#!/bin/sh
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Root supervisor of the A2A hub: owns the firewall, runs the proxy unprivileged.
set -eu

HUB_BIN="/usr/local/bin/enclave-a2a-hub"
SNAPSHOT_DIR="${ENCLAVE_A2A_HUB_SNAPSHOT_DIR:-/run/enclave-a2a/snapshots}"
STATUS_DIR="${ENCLAVE_A2A_HUB_STATUS_DIR:-/run/enclave-a2a/status}"
PROXY_UID="${ENCLAVE_A2A_PROXY_UID:-65532}"
GROUP_GENERATION="${ENCLAVE_A2A_GROUP_GENERATION:-}"
WORK_DIR="/run/enclave-a2a-hub"
IP_FORWARD_FILE="/proc/sys/net/ipv4/ip_forward"
# 0.1 s ticks to wait for the proxy's first status write.
PROXY_STATUS_TICKS=300
STATUS_ERROR_MAX_BYTES=512

EPOCH=""
PROXY_PID=""
RELOAD_PENDING="0"
APPLIED_REVISION=0

log() {
    echo "[enclave-a2a-hub] $*"
}

is_pid_running() {
    [ -n "$1" ] || return 1
    kill -0 "$1" >/dev/null 2>&1
}

new_epoch() {
    od -An -N8 -tx1 /dev/urandom | tr -d ' \n'
}

json_escape() {
    printf '%s' "$1" | tr '\n\t\r' '   ' | tr -d '\000-\037\177' | sed 's/\\/\\\\/g; s/"/\\"/g'
}

# write_status <applied> <rejected> <error> replaces supervisor.json atomically.
write_status() {
    status_file="$STATUS_DIR/supervisor.json"
    tmp="$STATUS_DIR/.supervisor.json.tmp-$$"
    err=$(printf '%s' "$3" | head -c "$STATUS_ERROR_MAX_BYTES")
    if ! {
        printf '{\n'
        printf '  "schemaVersion": "1",\n'
        printf '  "reader": "supervisor",\n'
        printf '  "groupGeneration": "%s",\n' "$(json_escape "$GROUP_GENERATION")"
        printf '  "epoch": "%s",\n' "$EPOCH"
        printf '  "appliedRevision": %s,\n' "$1"
        printf '  "rejectedRevision": %s,\n' "$2"
        printf '  "error": "%s",\n' "$(json_escape "$err")"
        printf '  "updatedAt": "%s"\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
        printf '}\n'
    } >"$tmp"; then
        rm -f "$tmp"
        return 1
    fi
    chmod 0644 "$tmp"
    sync "$tmp" 2>/dev/null || true
    if ! mv -f "$tmp" "$status_file"; then
        rm -f "$tmp"
        return 1
    fi
    sync "$STATUS_DIR" 2>/dev/null || true
}

# The fixed policy holds until the first snapshot is applied, and stays when none is valid.
install_default_deny() {
    for restore in iptables-restore ip6tables-restore; do
        if ! printf '%s\n' '*filter' ':INPUT DROP [0:0]' ':FORWARD DROP [0:0]' ':OUTPUT DROP [0:0]' \
            '-A INPUT -i lo -j ACCEPT' '-A OUTPUT -o lo -j ACCEPT' 'COMMIT' | "$restore"; then
            log "Failed to install default-deny policy ($restore)"
            return 1
        fi
    done
}

ensure_forwarding_off() {
    if [ "$(cat "$IP_FORWARD_FILE" 2>/dev/null || true)" = "0" ]; then
        return 0
    fi
    echo 0 >"$IP_FORWARD_FILE" 2>/dev/null || true
    if [ "$(cat "$IP_FORWARD_FILE" 2>/dev/null || true)" != "0" ]; then
        log "IPv4 forwarding is enabled and cannot be disabled; start the hub with net.ipv4.ip_forward=0"
        return 1
    fi
}

# render_rules_valid <file> refuses rule sets that would open a filter policy.
render_rules_valid() {
    for chain in INPUT FORWARD OUTPUT; do
        if ! grep -qx ":$chain DROP \[0:0\]" "$1"; then
            echo "rendered rules do not set $chain policy DROP"
            return 1
        fi
    done
    if [ "$(grep -c '^\*' "$1")" != "1" ] || ! grep -qx '\*filter' "$1"; then
        echo "rendered rules must contain only the filter table"
        return 1
    fi
}

# with_loopback <in> <out> keeps loopback open (enclave-a2a-hub call) whatever is rendered.
with_loopback() {
    awk '$0 == "COMMIT" { print "-I INPUT 1 -i lo -j ACCEPT"; print "-I OUTPUT 1 -o lo -j ACCEPT" } { print }' "$1" >"$2"
}

# endpoint_addresses <rules> lists peer addresses: INPUT sources and OUTPUT destinations.
endpoint_addresses() {
    awk '$1 == "-A" && ($2 == "INPUT" || $2 == "OUTPUT") {
        for (i = 3; i < NF; i++) {
            if (($2 == "INPUT" && $i == "-s") || ($2 == "OUTPUT" && $i == "-d")) {
                addr = $(i + 1)
                sub(/\/32$/, "", addr)
                if (addr ~ /^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/) print addr
            }
        }
    }' "$1" | sort -u
}

# removed_addresses <previous> <next> lists addresses only in previous.
removed_addresses() {
    awk 'FILENAME == ARGV[1] { keep[$0] = 1; next } !($0 in keep)' "$2" "$1"
}

# flush_endpoint aborts hub sockets before the conntrack flush so the RST still matches its entry.
flush_endpoint() {
    ss -K dst "$1" >/dev/null 2>&1 || log "ss -K dst $1 failed"
    conntrack -D -s "$1" >/dev/null 2>&1 || true
    conntrack -D -d "$1" >/dev/null 2>&1 || true
}

# send_gratuitous_arp announces every hub address; attach order and MACs change on reconnect.
send_gratuitous_arp() {
    garp_pids=""
    for entry in $(ip -o -4 addr show 2>/dev/null | awk '{ split($4, a, "/"); sub(/@.*/, "", $2); if ($2 != "lo") print $2 "," a[1] }'); do
        arping -U -c 1 -w 1 -I "${entry%%,*}" "${entry#*,}" >/dev/null 2>&1 &
        garp_pids="$garp_pids $!"
    done
    for pid in $garp_pids; do
        while is_pid_running "$pid"; do
            wait "$pid" 2>/dev/null || true
        done
    done
}

render_revision() {
    sed -n 's/^revision=\([0-9][0-9]*\)$/\1/p' "$1" | tail -n 1
}

render_error() {
    grep -v '^revision=[0-9]*$' "$1" | tail -n 1
}

current_revision() {
    target=$(readlink "$SNAPSHOT_DIR/current" 2>/dev/null || true)
    case "${target##*/}" in
        '' | *[!0-9]*) echo 0 ;;
        *) echo "${target##*/}" ;;
    esac
}

# reject <revision> <error> keeps the installed rules and reports the refusal.
reject() {
    log "Rejected revision $1: $2; keeping revision $APPLIED_REVISION"
    write_status "$APPLIED_REVISION" "$1" "$2"
}

# apply_current installs the rules rendered from current and flushes removed endpoints.
apply_current() {
    rendered="$WORK_DIR/rules.rendered"
    if ! "$HUB_BIN" render-rules --snapshot-dir "$SNAPSHOT_DIR" >"$rendered" 2>"$WORK_DIR/render.err"; then
        revision=$(render_revision "$WORK_DIR/render.err")
        reason=$(render_error "$WORK_DIR/render.err")
        reject "${revision:-$(current_revision)}" "${reason:-render-rules failed}"
        return
    fi
    revision=$(render_revision "$WORK_DIR/render.err")
    if [ -z "$revision" ]; then
        reject "$(current_revision)" "render-rules reported no revision"
        return
    fi
    if ! reason=$(render_rules_valid "$rendered"); then
        reject "$revision" "$reason"
        return
    fi
    with_loopback "$rendered" "$WORK_DIR/rules.v4"
    if ! iptables-restore <"$WORK_DIR/rules.v4" 2>"$WORK_DIR/restore.err"; then
        reject "$revision" "iptables-restore failed: $(tail -n 1 "$WORK_DIR/restore.err")"
        return
    fi
    endpoint_addresses "$WORK_DIR/rules.v4" >"$WORK_DIR/endpoints.next"
    for addr in $(removed_addresses "$WORK_DIR/endpoints" "$WORK_DIR/endpoints.next"); do
        flush_endpoint "$addr"
    done
    mv -f "$WORK_DIR/endpoints.next" "$WORK_DIR/endpoints"
    APPLIED_REVISION="$revision"
    log "Applied revision $revision"
    write_status "$APPLIED_REVISION" 0 ""
}

status_epoch() {
    sed -n 's/^[[:space:]]*"epoch"[[:space:]]*:[[:space:]]*"\([0-9a-f]*\)".*/\1/p' "$1" 2>/dev/null | head -n 1
}

start_proxy() {
    previous_proxy_epoch=$(status_epoch "$STATUS_DIR/proxy.json")
    su-exec "$PROXY_UID:$PROXY_UID" env ENCLAVE_A2A_HUB_EPOCH="$EPOCH" "$HUB_BIN" proxy &
    PROXY_PID=$!
    log "Started proxy (pid $PROXY_PID, uid $PROXY_UID)"
}

# wait_for_proxy_status returns once proxy.json carries an epoch of this run; the proxy's SIGHUP handler exists by then.
wait_for_proxy_status() {
    ticks=0
    while [ "$ticks" -lt "$PROXY_STATUS_TICKS" ]; do
        if ! is_pid_running "$PROXY_PID"; then
            log "Proxy exited before its first status write"
            return 1
        fi
        epoch=$(status_epoch "$STATUS_DIR/proxy.json")
        if [ -n "$epoch" ] && [ "$epoch" != "$previous_proxy_epoch" ]; then
            return 0
        fi
        ticks=$((ticks + 1))
        sleep 0.1
    done
    log "Proxy wrote no status within $((PROXY_STATUS_TICKS / 10)) s"
    return 1
}

reload_hub() {
    apply_current || return 1
    kill -HUP "$PROXY_PID" 2>/dev/null || true
    send_gratuitous_arp
}

start_hub() {
    umask 022
    mkdir -p "$WORK_DIR"
    chmod 0700 "$WORK_DIR"
    : >"$WORK_DIR/endpoints"
    EPOCH=$(new_epoch)
    install_default_deny || return 1
    ensure_forwarding_off || return 1
    log "Starting epoch $EPOCH"
    start_proxy
    wait_for_proxy_status || return 1
    apply_current || return 1
    send_gratuitous_arp
}

shutdown_hub() {
    trap - HUP TERM INT
    if is_pid_running "$PROXY_PID"; then
        kill -TERM "$PROXY_PID" 2>/dev/null || true
        wait "$PROXY_PID" 2>/dev/null || true
    fi
    exit "$1"
}

on_hup() {
    RELOAD_PENDING="1"
}

monitor_loop() {
    while :; do
        if [ "$RELOAD_PENDING" = "1" ]; then
            RELOAD_PENDING="0"
            if ! reload_hub; then
                log "Cannot write supervisor status; shutting down"
                shutdown_hub 1
            fi
        fi
        if ! is_pid_running "$PROXY_PID"; then
            log "Proxy exited unexpectedly"
            shutdown_hub 1
        fi
        sleep 0.1
    done
}

main() {
    trap on_hup HUP
    trap 'shutdown_hub 0' TERM INT
    if ! start_hub; then
        shutdown_hub 1
    fi
    log "Hub ready"
    monitor_loop
}

main "$@"
