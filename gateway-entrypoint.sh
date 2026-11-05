#!/bin/sh
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -eu

DNSMASQ_BIN="/usr/local/libexec/enclave/dnsmasq"
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
DNS_AUDIT_LOG_FILE="/var/log/enclave/dns-audit.log"
PROXY_READY_FILE="/var/lib/enclave/proxy/proxy.ready"
A2A_OVERLAY_FILE="/etc/enclave-a2a-overlay/overlay.json"
A2A_OVERLAY_MAX_BYTES="4096"

DNSMASQ_PID=""
PROXY_PID=""
DNS_AUDIT_PID=""
DNS_AUDIT_RESTARTS="0"
RELOAD_PENDING="0"

# Overlay state of the current read; PENDING means a marker is still owed.
A2A_OVERLAY_PENDING="false"
A2A_OVERLAY_ACTIVE="false"
A2A_OVERLAY_GENERATION=""
A2A_HUB_ADDRESS=""
A2A_HUB_PORT=""
A2A_ADAPTER_PORT=""
A2A_DNS_NAME=""

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
    : > "$DNS_AUDIT_LOG_FILE"
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

a2a_overlay_marker() {
    log "A2A overlay $1 (generation=$A2A_OVERLAY_GENERATION)"
}

# Logs the apply failure marker if the current overlay read still owes one.
a2a_overlay_apply_failed() {
    if [ "$A2A_OVERLAY_PENDING" = "true" ]; then
        A2A_OVERLAY_PENDING="false"
        a2a_overlay_marker "apply failed"
    fi
}

a2a_overlay_succeeded() {
    if [ "$A2A_OVERLAY_PENDING" != "true" ]; then
        return 0
    fi
    A2A_OVERLAY_PENDING="false"
    if [ "$A2A_OVERLAY_ACTIVE" = "true" ]; then
        a2a_overlay_marker "applied"
    else
        a2a_overlay_marker "cleared"
    fi
}

a2a_is_uint() {
    case "$1" in
        0) return 0 ;;
        ''|0*|*[!0-9]*) return 1 ;;
    esac
    [ "${#1}" -le 20 ]
}

a2a_is_port() {
    a2a_is_uint "$1" && [ "${#1}" -le 5 ] && [ "$1" -ge 1 ] && [ "$1" -le 65535 ]
}

a2a_is_unicast_ipv4() {
    echo "$1" | grep -Eq '^(0|[1-9][0-9]{0,2})(\.(0|[1-9][0-9]{0,2})){3}$' || return 1
    a2a_old_ifs="$IFS"
    IFS=.
    # shellcheck disable=SC2086 # Split the validated dotted quad into octets.
    set -- $1
    IFS="$a2a_old_ifs"
    for a2a_octet in "$@"; do
        [ "$a2a_octet" -le 255 ] || return 1
    done
    case "$1" in
        0|127|22[4-9]|23[0-9]|24[0-9]|25[0-5]) return 1 ;;
    esac
    return 0
}

a2a_is_dns_name() {
    [ "${#1}" -le 253 ] || return 1
    echo "$1" | grep -Eq '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$'
}

# Prints the raw value of a top-level key that occurs exactly once.
a2a_overlay_field() {
    a2a_count=$(printf '%s' "$1" | grep -o "\"$2\"[[:space:]]*:" | wc -l)
    [ "$a2a_count" -eq 1 ] || return 1
    a2a_value=$(printf '%s\n' "$1" | sed -n 's/.*"'"$2"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
    if [ -z "$a2a_value" ]; then
        a2a_value=$(printf '%s\n' "$1" | sed -n 's/.*"'"$2"'"[[:space:]]*:[[:space:]]*\([0-9a-z]*\).*/\1/p')
    fi
    [ -n "$a2a_value" ] || return 1
    printf '%s\n' "$a2a_value"
}

# A missing overlay means no A2A rules and no marker; an invalid one logs the
# validation marker and returns 1.
load_a2a_overlay() {
    A2A_OVERLAY_PENDING="false"
    A2A_OVERLAY_ACTIVE="false"
    A2A_OVERLAY_GENERATION=""
    A2A_HUB_ADDRESS=""
    A2A_HUB_PORT=""
    A2A_ADAPTER_PORT=""
    A2A_DNS_NAME=""

    if [ ! -e "$A2A_OVERLAY_FILE" ]; then
        return 0
    fi
    A2A_OVERLAY_GENERATION="unknown"

    if ! parse_a2a_overlay; then
        A2A_OVERLAY_ACTIVE="false"
        a2a_overlay_marker "validation failed"
        return 1
    fi
    A2A_OVERLAY_PENDING="true"
    return 0
}

parse_a2a_overlay() {
    [ -f "$A2A_OVERLAY_FILE" ] && [ -r "$A2A_OVERLAY_FILE" ] || return 1
    a2a_size=$(wc -c < "$A2A_OVERLAY_FILE") || return 1
    [ "$a2a_size" -le "$A2A_OVERLAY_MAX_BYTES" ] || return 1
    a2a_json=$(tr -d '\n\r\t' < "$A2A_OVERLAY_FILE") || return 1
    # No valid value needs escaping, so rejecting backslashes keeps the field matcher exact.
    case "$a2a_json" in
        *\\*) return 1 ;;
        \{*\}) ;;
        *) return 1 ;;
    esac

    a2a_generation=$(a2a_overlay_field "$a2a_json" generation) || return 1
    a2a_is_uint "$a2a_generation" || return 1
    A2A_OVERLAY_GENERATION="$a2a_generation"

    a2a_schema=$(a2a_overlay_field "$a2a_json" schemaVersion) || return 1
    [ "$a2a_schema" = "1" ] || return 1

    a2a_active=$(a2a_overlay_field "$a2a_json" active) || return 1
    case "$a2a_active" in
        false) return 0 ;;
        true) ;;
        *) return 1 ;;
    esac

    a2a_hub_address=$(a2a_overlay_field "$a2a_json" hubAddress) || return 1
    a2a_is_unicast_ipv4 "$a2a_hub_address" || return 1
    a2a_hub_port=$(a2a_overlay_field "$a2a_json" hubPort) || return 1
    a2a_is_port "$a2a_hub_port" || return 1
    a2a_adapter_port=$(a2a_overlay_field "$a2a_json" adapterPort) || return 1
    if [ "$a2a_adapter_port" != "0" ]; then
        a2a_is_port "$a2a_adapter_port" || return 1
    fi
    a2a_dns_name=$(a2a_overlay_field "$a2a_json" dnsName) || return 1
    a2a_is_dns_name "$a2a_dns_name" || return 1

    A2A_OVERLAY_ACTIVE="true"
    A2A_HUB_ADDRESS="$a2a_hub_address"
    A2A_HUB_PORT="$a2a_hub_port"
    A2A_ADAPTER_PORT="$a2a_adapter_port"
    A2A_DNS_NAME="$a2a_dns_name"
    return 0
}

# host-record answers only the exact name and, like a hosts entry, outranks address=/#/.
append_a2a_overlay_dns() {
    if [ "$A2A_OVERLAY_ACTIVE" != "true" ]; then
        return 0
    fi
    printf '\nhost-record=%s,%s\n' "$A2A_DNS_NAME" "$A2A_HUB_ADDRESS" >> "$1"
}

setup_a2a_input_rules() {
    if [ "$A2A_OVERLAY_ACTIVE" != "true" ]; then
        return 0
    fi
    if [ "$A2A_ADAPTER_PORT" != "0" ]; then
        setup_a2a_adapter_accept || return 1
    fi
    # A compromised hub must not reach published ports, which accept any source.
    iptables -A INPUT -s "$A2A_HUB_ADDRESS" -j DROP
}

setup_a2a_adapter_accept() {
    if ! command -v enclave_route_interface_ipv4 >/dev/null 2>&1; then
        log "A2A overlay: network helper unavailable"
        return 1
    fi
    a2a_bridge_if=$(enclave_route_interface_ipv4 "$A2A_HUB_ADDRESS" || true)
    if [ -z "$a2a_bridge_if" ] || [ "$a2a_bridge_if" = "lo" ]; then
        log "A2A overlay: no interface routes to hub address $A2A_HUB_ADDRESS"
        return 1
    fi
    iptables -A INPUT -i "$a2a_bridge_if" -s "$A2A_HUB_ADDRESS" -p tcp --dport "$A2A_ADAPTER_PORT" -j ACCEPT
}

setup_a2a_output_rule() {
    if [ "$A2A_OVERLAY_ACTIVE" != "true" ]; then
        return 0
    fi
    iptables -A OUTPUT -d "$A2A_HUB_ADDRESS" -p tcp --dport "$A2A_HUB_PORT" -j ACCEPT
}

setup_a2a_nat_exemption() {
    if [ "$A2A_OVERLAY_ACTIVE" != "true" ]; then
        return 0
    fi
    iptables -t nat -A OUTPUT -d "$A2A_HUB_ADDRESS" -j RETURN
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
    if validation_output=$("$DNSMASQ_BIN" --test --conf-file="$conf_path" 2>&1); then
        return 0
    fi
    log "dnsmasq config validation failed"
    if [ -n "$validation_output" ]; then
        printf '%s\n' "$validation_output" | sed '/./,$!d' >&2
    fi
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
    append_a2a_overlay_dns "$target_dir/dnsmasq.conf" || return 1
    validate_dnsmasq_conf "$target_dir/dnsmasq.conf" || return 1
    load_resolvers_from_conf "$target_dir/dnsmasq.conf"

    return 0
}

install_bundle() {
    target_dir="$1"
    mv "$target_dir/dnsmasq.conf" "$DNSMASQ_CONF" || return 1
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

    if ! command -v enclave_resolve_docker_host_ipv4 >/dev/null 2>&1; then
        log "IDE bridge: network helper unavailable; disabled"
        return
    fi

    ide_host_ip=$(enclave_resolve_docker_host_ipv4 || true)
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

setup_input_firewall() {
    firewall="$1"
    # Set the policy before flushing so reloads never admit new peer traffic.
    "$firewall" -P INPUT DROP || return 1
    "$firewall" -F INPUT || return 1
    "$firewall" -A INPUT -i lo -j ACCEPT || return 1
    "$firewall" -A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT || return 1
    if [ "$firewall" = "iptables" ]; then
        setup_a2a_input_rules || return 1
    fi

    published_ports=$(printf '%s' "${ENCLAVE_GATEWAY_PUBLISHED_PORTS:-}" | tr ',' '\n')
    while IFS= read -r published; do
        [ -z "$published" ] && continue
        port=${published%/*}
        protocol=${published##*/}
        case "$protocol" in
            tcp|udp) ;;
            *) log "Invalid published port protocol: $published"; return 1 ;;
        esac
        case "$port" in
            ''|*[!0-9]*) log "Invalid published port: $published"; return 1 ;;
        esac
        if ! [ "$port" -ge 1 ] || ! [ "$port" -le 65535 ]; then
            log "Published port out of range: $published"
            return 1
        fi
        "$firewall" -A INPUT -p "$protocol" --dport "$port" -j ACCEPT || return 1
    done <<EOF_PUBLISHED
$published_ports
EOF_PUBLISHED
}

setup_firewall() {
    setup_input_firewall iptables || return 1
    iptables -P OUTPUT DROP || return 1
    iptables -F OUTPUT || return 1

    iptables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT || return 1
    iptables -A OUTPUT -o lo -j ACCEPT || return 1
    setup_a2a_output_rule || return 1

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
    setup_a2a_nat_exemption || return 1
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
        setup_input_firewall ip6tables || return 1
        ip6tables -P OUTPUT DROP || return 1
        ip6tables -F OUTPUT || return 1
        ip6tables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT || return 1
        ip6tables -A OUTPUT -o lo -j ACCEPT || return 1
    fi
}

enforce_fail_closed_lockdown() {
    # Keep the shared network namespace fail-closed even if gateway processes
    # crash or reload validation fails before the main container exits.
    iptables -P INPUT DROP || true
    iptables -F INPUT || true
    iptables -P OUTPUT DROP || true
    iptables -F OUTPUT || true

    if command -v ip6tables >/dev/null 2>&1; then
        ip6tables -P INPUT DROP || true
        ip6tables -F INPUT || true
        ip6tables -P OUTPUT DROP || true
        ip6tables -F OUTPUT || true
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
        # The proxy creates its container-private leaf cache under this TLS root.
        chown "$PROXY_USER:$PROXY_USER" "$proxy_data" "$proxy_tls" 2>/dev/null || true
        chown "$PROXY_USER:$PROXY_USER" "$proxy_tls/ca.crt" "$proxy_tls/ca.key" 2>/dev/null || true
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
        su-exec "$DNSMASQ_USER:$DNSMASQ_USER" "$DNSMASQ_BIN" --keep-in-foreground --conf-file="$DNSMASQ_CONF" --log-queries --log-facility="$DNSMASQ_LOG_FILE" &
    else
        su-exec "$DNSMASQ_USER:$DNSMASQ_USER" "$DNSMASQ_BIN" --keep-in-foreground --conf-file="$DNSMASQ_CONF" &
    fi
    DNSMASQ_PID="$!"
}

# The translator writes JSONL audit events, so the network log stays valid
# JSONL and DNS denials are machine readable. It runs whether or not the proxy
# is enabled, because DNS denials must be recorded either way.
start_dns_audit() {
    if [ -z "$DNSMASQ_LOG_FILE" ]; then
        return
    fi

    log "Starting DNS audit translator"
    # Unprivileged like the other long-lived children: it only reads the dnsmasq
    # log and appends to the world-writable network log.
    su-exec "$PROXY_USER:$PROXY_USER" enclave-gateway-proxy -dns-audit "$DNSMASQ_LOG_FILE" >>"$DNS_AUDIT_LOG_FILE" 2>&1 &
    DNS_AUDIT_PID="$!"
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

    if ! load_a2a_overlay; then
        log "Reload validation failed (generation=$generation)"
        return 1
    fi

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
        a2a_overlay_apply_failed
        log "Reload validation failed (generation=$generation)"
        return 1
    fi

    if ! install_bundle "$tmp_dir"; then
        restore_bundle "$rollback_dir"
        rm -rf "$tmp_dir"
        a2a_overlay_apply_failed
        log "Reload apply failed (generation=$generation)"
        return 1
    fi

    if ! atomic_swap_ipset; then
        restore_bundle "$rollback_dir"
        rm -rf "$tmp_dir"
        a2a_overlay_apply_failed
        log "Reload ipset swap failed (generation=$generation)"
        return 1
    fi

    if ! setup_firewall; then
        restore_bundle "$rollback_dir"
        reload_children >/dev/null 2>&1 || true
        rm -rf "$tmp_dir"
        a2a_overlay_apply_failed
        log "Reload apply failed (generation=$generation)"
        return 1
    fi

    if ! reload_children; then
        restore_bundle "$rollback_dir"
        reload_children >/dev/null 2>&1 || true
        rm -rf "$tmp_dir"
        a2a_overlay_apply_failed
        log "Reload process restart failed (generation=$generation)"
        return 1
    fi

    rm -rf "$tmp_dir"
    a2a_overlay_succeeded
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

    if is_pid_running "$DNS_AUDIT_PID"; then
        stop_child_pid "$DNS_AUDIT_PID" "dns-audit"
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

        # Auditing is not an enforcement path, so a dead translator must not
        # take egress down with it. Restart it a bounded number of times, then
        # say plainly that DNS events are no longer recorded.
        if [ -n "$DNS_AUDIT_PID" ] && ! is_pid_running "$DNS_AUDIT_PID"; then
            if [ "$DNS_AUDIT_RESTARTS" -lt 3 ]; then
                DNS_AUDIT_RESTARTS=$((DNS_AUDIT_RESTARTS + 1))
                log "DNS audit translator exited; restarting (attempt $DNS_AUDIT_RESTARTS)"
                start_dns_audit
            else
                log "DNS audit translator keeps exiting; DNS denials are no longer recorded (see $DNS_AUDIT_LOG_FILE)"
                DNS_AUDIT_PID=""
            fi
        fi

        sleep 0.1
    done
}

startup_failed() {
    a2a_overlay_apply_failed
    exit 1
}

main() {
    trap on_hup HUP
    trap on_term TERM INT

    normalize_proxy_enabled
    init_log_files
    ensure_local_resolver

    if ! load_a2a_overlay; then
        exit 1
    fi

    tmp_dir=$(mktemp -d /tmp/enclave-gateway-init.XXXXXX)
    if ! render_and_validate_bundle "$tmp_dir"; then
        rm -rf "$tmp_dir"
        startup_failed
    fi
    install_bundle "$tmp_dir" || startup_failed
    rm -rf "$tmp_dir"

    startup_generation=$(read_bundle_generation || true)
    if [ -n "$startup_generation" ]; then
        log "Loaded gateway bundle generation=$startup_generation"
    fi

    create_ipset || startup_failed
    if ! setup_firewall; then
        startup_failed
    fi
    setup_kernel_network || startup_failed

    if ! prepare_proxy_runtime_data; then
        startup_failed
    fi
    if ! start_proxy; then
        startup_failed
    fi
    start_dnsmasq
    start_dns_audit

    a2a_overlay_succeeded
    log "Gateway ready"
    monitor_loop
}

main "$@"
