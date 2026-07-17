#!/bin/sh
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -eu

DNSMASQ_CONF="/etc/dnsmasq.d/enclave.conf"
ACTIVE_DOMAINS_FILE="/run/enclave-gateway/domains.txt"
IPSET_NAME="enclave_allowed"
DNSMASQ_USER="dnsmasq"
PROXY_USER="gateway"
PROXY_ENABLED="${PROXY_ENABLED:-true}"
NETWORK_LOG_FILE="${ENCLAVE_NETWORK_LOG_FILE:-}"
GATEWAY_CONFIG_DIR="${ENCLAVE_GATEWAY_CONFIG_DIR:-/etc/enclave-gateway-config}"
GATEWAY_CONFIG_DNSMASQ="$GATEWAY_CONFIG_DIR/dnsmasq.conf"
GATEWAY_CONFIG_DOMAINS="$GATEWAY_CONFIG_DIR/domains.txt"
GATEWAY_CONFIG_META="$GATEWAY_CONFIG_DIR/meta.json"
DNSMASQ_LOG_FILE=""
PROXY_LOG_FILE="/var/log/enclave/proxy.log"
PROXY_READY_FILE="/var/lib/enclave/proxy/proxy.ready"

DNSMASQ_PID=""
PROXY_PID=""
DNSMASQ_TAIL_PID=""
RELOAD_PENDING="0"

UNIQUE_RESOLVERS=""

log() {
    echo "[enclave-gateway] $*"
}

enclave_net_lib="${ENCLAVE_NET_LIB:-/usr/local/share/enclave/net.sh}"
if [ -r "$enclave_net_lib" ]; then
    # shellcheck disable=SC1090 # Runtime asset path can be overridden by tests.
    . "$enclave_net_lib"
fi

is_ipv4() {
    echo "$1" | grep -Eq '^[0-9]+(\.[0-9]+){3}$'
}

is_pid_running() {
    pid="$1"
    [ -n "$pid" ] || return 1
    kill -0 "$pid" >/dev/null 2>&1
}

stop_child_pid() {
    pid="$1"
    name="$2"

    if ! is_pid_running "$pid"; then
        return 0
    fi

    kill -TERM "$pid" >/dev/null 2>&1 || true

    attempts=0
    while [ "$attempts" -lt 10 ]; do
        if ! is_pid_running "$pid"; then
            wait "$pid" 2>/dev/null || true
            return 0
        fi
        attempts=$((attempts + 1))
        sleep 0.1
    done

    log "$name did not stop after TERM; sending KILL"
    kill -KILL "$pid" >/dev/null 2>&1 || true
    wait "$pid" 2>/dev/null || true
    return 0
}

wait_for_proxy_ready() {
    attempts=0
    while [ "$attempts" -lt 50 ]; do
        if ! is_pid_running "$PROXY_PID"; then
            log "Proxy failed to start"
            cat "$PROXY_LOG_FILE" >&2 || true
            return 1
        fi
        if [ -f "$PROXY_READY_FILE" ]; then
            return 0
        fi
        attempts=$((attempts + 1))
        sleep 0.1
    done

    log "Proxy readiness timed out"
    cat "$PROXY_LOG_FILE" >&2 || true
    return 1
}

normalize_proxy_enabled() {
    case "$PROXY_ENABLED" in
        0|false|disabled|off|no)
            PROXY_ENABLED="false"
            ;;
        *)
            PROXY_ENABLED="true"
            ;;
    esac
}

init_log_files() {
    mkdir -p /var/log/enclave /run/enclave-gateway

    if [ -n "$NETWORK_LOG_FILE" ]; then
        mkdir -p "$(dirname "$NETWORK_LOG_FILE")"
        touch "$NETWORK_LOG_FILE"
        # Proxy runs as an unprivileged user and appends to this bind-mounted path.
        chmod 666 "$NETWORK_LOG_FILE" 2>/dev/null || true
        DNSMASQ_LOG_FILE="/var/log/enclave/dnsmasq.log"
        touch "$DNSMASQ_LOG_FILE"
        chown "$DNSMASQ_USER:$DNSMASQ_USER" "$DNSMASQ_LOG_FILE" 2>/dev/null || true
    fi

    : > "$PROXY_LOG_FILE"
    chown "$PROXY_USER:$PROXY_USER" "$PROXY_LOG_FILE" 2>/dev/null || true
}

host_bundle_complete() {
    [ -f "$GATEWAY_CONFIG_DNSMASQ" ] || return 1
    [ -f "$GATEWAY_CONFIG_DOMAINS" ] || return 1
    [ -f "$GATEWAY_CONFIG_META" ] || return 1
    return 0
}

load_resolvers_from_conf() {
    conf_path="$1"
    parsed_resolvers=""

    while IFS= read -r raw_line || [ -n "$raw_line" ]; do
        line="${raw_line%%#*}"
        line=$(printf '%s' "$line" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
        [ -z "$line" ] && continue

        case "$line" in
            server=/*/*)
                resolver=$(echo "$line" | cut -d/ -f3)
                resolver=${resolver%%#*}
                resolver=${resolver%%:*}
                if is_ipv4 "$resolver"; then
                    parsed_resolvers="${parsed_resolvers}${parsed_resolvers:+
}$resolver"
                fi
                ;;
        esac
    done < "$conf_path"

    UNIQUE_RESOLVERS=""
    if [ -n "$parsed_resolvers" ]; then
        UNIQUE_RESOLVERS=$(printf '%s\n' "$parsed_resolvers" | sort -u)
    fi

    if [ -z "$UNIQUE_RESOLVERS" ]; then
        UNIQUE_RESOLVERS="1.1.1.1
8.8.8.8"
        log "No resolvers parsed from host bundle; falling back to 1.1.1.1 and 8.8.8.8"
    fi
}

copy_host_bundle() {
    target_dir="$1"
    cp "$GATEWAY_CONFIG_DNSMASQ" "$target_dir/dnsmasq.conf"
    cp "$GATEWAY_CONFIG_DOMAINS" "$target_dir/domains.txt"
    cp "$GATEWAY_CONFIG_META" "$target_dir/meta.json"
}

read_bundle_generation() {
    if [ ! -f "$GATEWAY_CONFIG_META" ]; then
        return 1
    fi
    generation=$(sed -n 's/.*"generation"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$GATEWAY_CONFIG_META" | head -n 1)
    [ -n "$generation" ] || return 1
    printf '%s\n' "$generation"
}

ensure_local_resolver() {
    if command -v enclave_ensure_local_resolver >/dev/null 2>&1; then
        enclave_ensure_local_resolver
        return
    fi
    if ! printf "nameserver 127.0.0.1\n" > /etc/resolv.conf 2>/dev/null; then
        log "Unable to update /etc/resolv.conf; DNS enforcement may be incomplete"
    fi
}

validate_dnsmasq_conf() {
    conf_path="$1"
    if dnsmasq --test --conf-file="$conf_path" >/dev/null 2>&1; then
        return 0
    fi
    log "dnsmasq config validation failed"
    return 1
}

render_and_validate_bundle() {
    target_dir="$1"

    if ! host_bundle_complete; then
        log "Host gateway bundle is incomplete in $GATEWAY_CONFIG_DIR"
        return 1
    fi

    log "Using host-managed gateway config bundle from $GATEWAY_CONFIG_DIR"
    copy_host_bundle "$target_dir"
    validate_dnsmasq_conf "$target_dir/dnsmasq.conf" || return 1
    load_resolvers_from_conf "$target_dir/dnsmasq.conf"

    return 0
}

install_bundle() {
    target_dir="$1"
    mv "$target_dir/dnsmasq.conf" "$DNSMASQ_CONF"
    mv "$target_dir/domains.txt" "$ACTIVE_DOMAINS_FILE"
}

restore_bundle() {
    rollback_dir="$1"
    if [ -f "$rollback_dir/dnsmasq.conf" ]; then
        cp "$rollback_dir/dnsmasq.conf" "$DNSMASQ_CONF"
    fi
    if [ -f "$rollback_dir/domains.txt" ]; then
        cp "$rollback_dir/domains.txt" "$ACTIVE_DOMAINS_FILE"
    fi
    if [ -f "$DNSMASQ_CONF" ]; then
        load_resolvers_from_conf "$DNSMASQ_CONF"
    fi
}

create_ipset() {
    ipset create "$IPSET_NAME" hash:net -exist
    ipset flush "$IPSET_NAME"
}

atomic_swap_ipset() {
    tmp_set="${IPSET_NAME}_reload"
    ipset destroy "$tmp_set" >/dev/null 2>&1 || true

    if ! ipset create "$tmp_set" hash:net; then
        log "failed to create temporary ipset $tmp_set"
        return 1
    fi

    if ! ipset swap "$tmp_set" "$IPSET_NAME"; then
        log "failed to swap ipset $tmp_set -> $IPSET_NAME"
        ipset destroy "$tmp_set" >/dev/null 2>&1 || true
        return 1
    fi

    ipset destroy "$tmp_set" >/dev/null 2>&1 || true
    return 0
}

setup_loopback_forwarding() {
    if [ -z "${ENCLAVE_LOOPBACK_PORTS:-}" ]; then
        return
    fi

    route_localnet="0"
    if command -v sysctl >/dev/null 2>&1; then
        route_localnet=$(sysctl -n net.ipv4.conf.all.route_localnet 2>/dev/null || echo "0")
    fi

    force_socat="false"
    if [ "$route_localnet" != "1" ]; then
        force_socat="true"
        log "route_localnet disabled; using socat for loopback ports"
    fi

    start_loopback_proxy() {
        port="$1"
        if ! command -v socat >/dev/null 2>&1; then
            log "socat unavailable; loopback redirect disabled for port $port"
            return
        fi
        if ! command -v enclave_resolve_bind_ip >/dev/null 2>&1 || ! command -v enclave_start_socat_loopback_proxy >/dev/null 2>&1; then
            log "network helper unavailable; loopback redirect disabled for port $port"
            return
        fi
        bind_ip=$(enclave_resolve_bind_ip || true)
        if [ -z "$bind_ip" ]; then
            log "Unable to determine container IP; loopback redirect disabled for port $port"
            return
        fi
        enclave_start_socat_loopback_proxy "$port" "$bind_ip"
        log "Loopback proxy (socat) enabled on port $port"
    }

    loopback_ports=$(printf '%s' "$ENCLAVE_LOOPBACK_PORTS" | tr ',' '\n')
    if [ -z "$loopback_ports" ]; then
        return
    fi

    while IFS= read -r port; do
        [ -z "$port" ] && continue
        if [ "$force_socat" = "true" ]; then
            start_loopback_proxy "$port"
            continue
        fi
        if iptables -t nat -L PREROUTING >/dev/null 2>&1; then
            if iptables -t nat -A PREROUTING -p tcp --dport "$port" -j DNAT --to-destination 127.0.0.1:"$port" >/dev/null 2>&1; then
                log "DNAT port $port to loopback listener"
            else
                log "Failed to add loopback redirect for port $port; using socat fallback"
                start_loopback_proxy "$port"
            fi
        else
            log "NAT PREROUTING unavailable; using socat fallback for port $port"
            start_loopback_proxy "$port"
        fi
    done <<EOF_LOOPBACK
$loopback_ports
EOF_LOOPBACK
}

setup_ide_bridge() {
    if [ -z "${ENCLAVE_IDE_BRIDGE_PORTS:-}" ]; then
        return
    fi

    ide_host_ip=$(getent hosts host.docker.internal 2>/dev/null | awk '{print $1}')
    if [ -z "$ide_host_ip" ]; then
        ide_host_ip=$(awk '/host\.docker\.internal/ {print $1; exit}' /etc/hosts)
    fi
    if [ -z "$ide_host_ip" ]; then
        log "IDE bridge: cannot resolve host.docker.internal; disabled"
        return
    fi

    ide_ports=$(printf '%s' "$ENCLAVE_IDE_BRIDGE_PORTS" | tr ',' '\n')
    while IFS= read -r port; do
        [ -z "$port" ] && continue
        if iptables -t nat -A OUTPUT -p tcp -d 127.0.0.1 --dport "$port" \
            -j DNAT --to-destination "${ide_host_ip}:${port}" 2>/dev/null && \
            iptables -t nat -A POSTROUTING -p tcp -d "$ide_host_ip" --dport "$port" \
            -j MASQUERADE 2>/dev/null && \
            iptables -A OUTPUT -p tcp -d "$ide_host_ip" --dport "$port" -j ACCEPT; then
            log "IDE bridge: port $port -> ${ide_host_ip}:${port}"
        else
            log "IDE bridge: failed DNAT for port $port"
        fi
    done <<EOF_IDE
$ide_ports
EOF_IDE
}

setup_firewall() {
    iptables -F OUTPUT || true
    iptables -F INPUT || true
    iptables -P INPUT ACCEPT
    iptables -P OUTPUT DROP

    iptables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
    iptables -A OUTPUT -o lo -j ACCEPT
    iptables -A INPUT -i lo -j ACCEPT

    iptables -A OUTPUT -p udp --dport 53 -d 127.0.0.1 -j ACCEPT
    iptables -A OUTPUT -p tcp --dport 53 -d 127.0.0.1 -j ACCEPT

    setup_loopback_forwarding

    owner_supported="true"
    if ! iptables -m owner -h >/dev/null 2>&1; then
        owner_supported="false"
        log "Owner match unavailable; DNS egress will be less restricted"
    fi

    dns_uid=$(id -u "$DNSMASQ_USER")
    while IFS= read -r resolver; do
        [ -z "$resolver" ] && continue
        if [ "$owner_supported" = "true" ]; then
            iptables -A OUTPUT -p udp --dport 53 -d "$resolver" -m owner --uid-owner "$dns_uid" -j ACCEPT
            iptables -A OUTPUT -p tcp --dport 53 -d "$resolver" -m owner --uid-owner "$dns_uid" -j ACCEPT
        else
            iptables -A OUTPUT -p udp --dport 53 -d "$resolver" -j ACCEPT
            iptables -A OUTPUT -p tcp --dport 53 -d "$resolver" -j ACCEPT
        fi
    done <<EOF_RESOLVERS
$UNIQUE_RESOLVERS
EOF_RESOLVERS

    iptables -A OUTPUT -m set --match-set "$IPSET_NAME" dst -j ACCEPT

    if [ "$PROXY_ENABLED" != "true" ]; then
        setup_ide_bridge
        return
    fi

    if [ "$owner_supported" != "true" ]; then
        log "Owner match unavailable; cannot safely enable proxy"
        return 1
    fi

    proxy_uid=$(id -u "$PROXY_USER")

    iptables -A OUTPUT -p tcp --dport 8080 -m addrtype --dst-type LOCAL -j ACCEPT
    iptables -A OUTPUT -p tcp --dport 8443 -m addrtype --dst-type LOCAL -j ACCEPT

    iptables -t nat -F OUTPUT || true
    iptables -t nat -A OUTPUT -p tcp --dport 80 -m owner --uid-owner "$proxy_uid" -j RETURN
    iptables -t nat -A OUTPUT -p tcp --dport 443 -m owner --uid-owner "$proxy_uid" -j RETURN
    iptables -t nat -A OUTPUT -p tcp --dport 80 -j REDIRECT --to-ports 8080
    iptables -t nat -A OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports 8443

    log "Transparent proxy NAT rules configured"
    setup_ide_bridge
}

setup_kernel_network() {
    if [ -d /proc/sys/net/ipv4/conf/all ]; then
        sysctl -w net.ipv4.conf.all.route_localnet=1 >/dev/null 2>&1 || true
    fi
    if [ -d /proc/sys/net/ipv4/conf/eth0 ]; then
        sysctl -w net.ipv4.conf.eth0.route_localnet=1 >/dev/null 2>&1 || true
    fi

    if command -v sysctl >/dev/null 2>&1; then
        sysctl -w net.ipv6.conf.all.disable_ipv6=1 >/dev/null 2>&1 || true
        sysctl -w net.ipv6.conf.default.disable_ipv6=1 >/dev/null 2>&1 || true
    fi
    if command -v ip6tables >/dev/null 2>&1; then
        ip6tables -F OUTPUT || true
        ip6tables -F INPUT || true
        ip6tables -P INPUT ACCEPT
        ip6tables -P OUTPUT DROP
        ip6tables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
        ip6tables -A OUTPUT -o lo -j ACCEPT
        ip6tables -A INPUT -i lo -j ACCEPT
    fi
}

enforce_fail_closed_lockdown() {
    # Keep the shared network namespace fail-closed even if gateway processes
    # crash or reload validation fails before the main container exits.
    iptables -F OUTPUT || true
    iptables -P OUTPUT DROP || true

    if command -v ip6tables >/dev/null 2>&1; then
        ip6tables -F OUTPUT || true
        ip6tables -P OUTPUT DROP || true
    fi
}

prepare_proxy_runtime_data() {
    if [ "$PROXY_ENABLED" != "true" ]; then
        return 0
    fi
    if ! command -v enclave-gateway-proxy >/dev/null 2>&1; then
        log "Proxy binary not found; aborting"
        return 1
    fi

    proxy_data="/var/lib/enclave/proxy"
    mkdir -p "$proxy_data"

    secret_release_file="${ENCLAVE_SECRET_RELEASE_FILE:-${ENCLAVE_SECRET_INJECTION_FILE:-}}"
    if [ -n "$secret_release_file" ]; then
        if [ ! -f "$secret_release_file" ]; then
            log "Secret release file missing: $secret_release_file"
            return 1
        fi
        cp "$secret_release_file" "$proxy_data/secret-releases.json"
        chown "$PROXY_USER:$PROXY_USER" "$proxy_data/secret-releases.json"
        chmod 600 "$proxy_data/secret-releases.json"
        ENCLAVE_SECRET_RELEASE_FILE="$proxy_data/secret-releases.json"
        export ENCLAVE_SECRET_RELEASE_FILE
    fi

    tls_root="${ENCLAVE_GATEWAY_TLS_ROOT:-${ENCLAVE_TLS_ROOT:-}}"
    if [ -n "$tls_root" ]; then
        proxy_tls="$proxy_data/tls"
        mkdir -p "$proxy_tls"
        [ -f "${tls_root}/ca.crt" ] && cp "${tls_root}/ca.crt" "$proxy_tls/"
        [ -f "${tls_root}/ca.key" ] && cp "${tls_root}/ca.key" "$proxy_tls/"
        if [ -d "${tls_root}/hosts" ]; then
            chown "$PROXY_USER:$PROXY_USER" "${tls_root}/hosts" 2>/dev/null || true
            ln -sfn "${tls_root}/hosts" "$proxy_tls/hosts"
        else
            mkdir -p "$proxy_tls/hosts"
        fi
        chown "$PROXY_USER:$PROXY_USER" "$proxy_data" "$proxy_tls" 2>/dev/null || true
        chown "$PROXY_USER:$PROXY_USER" "$proxy_tls/ca.crt" "$proxy_tls/ca.key" "$proxy_tls/hosts" 2>/dev/null || true
        chmod 600 "$proxy_tls/ca.key" 2>/dev/null || true
        ENCLAVE_GATEWAY_TLS_ROOT="$proxy_tls"
        ENCLAVE_TLS_ROOT="$proxy_tls"
        export ENCLAVE_GATEWAY_TLS_ROOT ENCLAVE_TLS_ROOT
    fi

    export ENCLAVE_GATEWAY_CONFIG_DIR=/run/enclave-gateway
    return 0
}

start_proxy() {
    if [ "$PROXY_ENABLED" != "true" ]; then
        return 0
    fi

    log "Starting transparent proxy"
    rm -f "$PROXY_READY_FILE"
    su-exec "$PROXY_USER:$PROXY_USER" env ENCLAVE_GATEWAY_PROXY_READY_FILE="$PROXY_READY_FILE" enclave-gateway-proxy >>"$PROXY_LOG_FILE" 2>&1 &
    PROXY_PID="$!"
    wait_for_proxy_ready
}

start_dnsmasq() {
    log "Starting dnsmasq"
    if [ -n "$DNSMASQ_LOG_FILE" ]; then
        su-exec "$DNSMASQ_USER:$DNSMASQ_USER" dnsmasq --keep-in-foreground --conf-file="$DNSMASQ_CONF" --log-queries --log-facility="$DNSMASQ_LOG_FILE" &
    else
        su-exec "$DNSMASQ_USER:$DNSMASQ_USER" dnsmasq --keep-in-foreground --conf-file="$DNSMASQ_CONF" &
    fi
    DNSMASQ_PID="$!"
}

start_dnsmasq_log_tail() {
    if [ -z "$DNSMASQ_LOG_FILE" ]; then
        return
    fi

    tail -n 0 -F "$DNSMASQ_LOG_FILE" | while IFS= read -r line; do
        case "$line" in
            *NXDOMAIN*|*NODATA*|*SERVFAIL*|*REFUSED*)
                printf '%s\n' "$line" >> "$NETWORK_LOG_FILE"
                ;;
        esac
    done &
    DNSMASQ_TAIL_PID="$!"
}

reload_children() {
    if [ "$PROXY_ENABLED" = "true" ] && ! is_pid_running "$PROXY_PID"; then
        log "Cannot reload: proxy process is not running"
        return 1
    fi

    if is_pid_running "$DNSMASQ_PID"; then
        stop_child_pid "$DNSMASQ_PID" "dnsmasq"
    fi
    start_dnsmasq
    if ! is_pid_running "$DNSMASQ_PID"; then
        log "dnsmasq failed to restart"
        return 1
    fi

    if [ "$PROXY_ENABLED" = "true" ]; then
        if is_pid_running "$PROXY_PID"; then
            stop_child_pid "$PROXY_PID" "proxy"
        fi
        start_proxy || return 1
    fi

    return 0
}

reload_gateway() {
    generation=$(read_bundle_generation || true)
    if [ -z "$generation" ]; then
        log "Reload requested but generation is missing in $GATEWAY_CONFIG_META"
        return 1
    fi
    log "Reload requested (generation=$generation)"

    tmp_dir=$(mktemp -d /tmp/enclave-gateway-reload.XXXXXX)
    rollback_dir="$tmp_dir/rollback"
    mkdir -p "$rollback_dir"

    if [ -f "$DNSMASQ_CONF" ]; then
        cp "$DNSMASQ_CONF" "$rollback_dir/dnsmasq.conf"
    fi
    if [ -f "$ACTIVE_DOMAINS_FILE" ]; then
        cp "$ACTIVE_DOMAINS_FILE" "$rollback_dir/domains.txt"
    fi

    if ! render_and_validate_bundle "$tmp_dir"; then
        rm -rf "$tmp_dir"
        log "Reload validation failed (generation=$generation)"
        return 1
    fi

    if ! install_bundle "$tmp_dir"; then
        restore_bundle "$rollback_dir"
        rm -rf "$tmp_dir"
        log "Reload apply failed (generation=$generation)"
        return 1
    fi

    if ! atomic_swap_ipset; then
        restore_bundle "$rollback_dir"
        rm -rf "$tmp_dir"
        log "Reload ipset swap failed (generation=$generation)"
        return 1
    fi

    if ! setup_firewall; then
        restore_bundle "$rollback_dir"
        reload_children >/dev/null 2>&1 || true
        rm -rf "$tmp_dir"
        log "Reload apply failed (generation=$generation)"
        return 1
    fi

    if ! reload_children; then
        restore_bundle "$rollback_dir"
        reload_children >/dev/null 2>&1 || true
        rm -rf "$tmp_dir"
        log "Reload process restart failed (generation=$generation)"
        return 1
    fi

    rm -rf "$tmp_dir"
    log "Reload completed (generation=$generation)"
    return 0
}

shutdown_gateway() {
    code="$1"
    reason="$2"

    trap - HUP TERM INT
    log "$reason"

    if [ "$code" -ne 0 ]; then
        enforce_fail_closed_lockdown
    fi

    if is_pid_running "$DNSMASQ_TAIL_PID"; then
        kill "$DNSMASQ_TAIL_PID" >/dev/null 2>&1 || true
        wait "$DNSMASQ_TAIL_PID" 2>/dev/null || true
    fi

    if is_pid_running "$PROXY_PID"; then
        stop_child_pid "$PROXY_PID" "proxy"
    fi

    if is_pid_running "$DNSMASQ_PID"; then
        stop_child_pid "$DNSMASQ_PID" "dnsmasq"
    fi

    exit "$code"
}

on_hup() {
    RELOAD_PENDING="1"
}

on_term() {
    shutdown_gateway 0 "Shutdown signal received"
}

monitor_loop() {
    while :; do
        if [ "$RELOAD_PENDING" = "1" ]; then
            RELOAD_PENDING="0"
            if ! reload_gateway; then
                shutdown_gateway 1 "Reload failed; shutting down gateway (fail-closed)"
            fi
        fi

        if ! is_pid_running "$DNSMASQ_PID"; then
            shutdown_gateway 1 "dnsmasq exited unexpectedly"
        fi

        if [ "$PROXY_ENABLED" = "true" ] && ! is_pid_running "$PROXY_PID"; then
            shutdown_gateway 1 "proxy exited unexpectedly"
        fi

        sleep 0.1
    done
}

main() {
    trap on_hup HUP
    trap on_term TERM INT

    normalize_proxy_enabled
    init_log_files
    ensure_local_resolver

    tmp_dir=$(mktemp -d /tmp/enclave-gateway-init.XXXXXX)
    if ! render_and_validate_bundle "$tmp_dir"; then
        rm -rf "$tmp_dir"
        exit 1
    fi
    install_bundle "$tmp_dir"
    rm -rf "$tmp_dir"

    startup_generation=$(read_bundle_generation || true)
    if [ -n "$startup_generation" ]; then
        log "Loaded gateway bundle generation=$startup_generation"
    fi

    create_ipset
    if ! setup_firewall; then
        exit 1
    fi
    setup_kernel_network

    if ! prepare_proxy_runtime_data; then
        exit 1
    fi
    if ! start_proxy; then
        exit 1
    fi
    start_dnsmasq
    start_dnsmasq_log_tail

    log "Gateway ready"
    monitor_loop
}

main "$@"
