# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# shellcheck shell=sh
# Shared network helpers for enclave entrypoints.

_enclave_net_log() {
    if command -v log >/dev/null 2>&1; then
        log "$*"
    else
        echo "$*"
    fi
}

enclave_ensure_local_resolver() {
    if [ "$(cat /etc/resolv.conf 2>/dev/null)" = "nameserver 127.0.0.1" ]; then
        return 0
    fi
    if ! { printf 'nameserver 127.0.0.1\n' > /etc/resolv.conf; } 2>/dev/null; then
        _enclave_net_log "Unable to update /etc/resolv.conf; DNS enforcement may be incomplete"
    fi
}

enclave_resolve_bind_ip() {
    _enclave_host="$(hostname 2>/dev/null || true)"
    if [ -n "$_enclave_host" ]; then
        _enclave_ip="$(awk -v host="$_enclave_host" '($1 ~ /^[0-9]/ && $2 == host) {print $1; exit}' /etc/hosts)"
        if [ -n "$_enclave_ip" ]; then
            printf '%s\n' "$_enclave_ip"
            return 0
        fi
    fi
    _enclave_ip="$(awk '($1 ~ /^[0-9]/ && $1 !~ /^127\./ && $2 != "host.docker.internal") {print $1; exit}' /etc/hosts)"
    if [ -n "$_enclave_ip" ]; then
        printf '%s\n' "$_enclave_ip"
        return 0
    fi
    return 1
}

enclave_start_socat_loopback_proxy() {
    _enclave_port="$1"
    _enclave_bind_ip="$2"
    socat "TCP-LISTEN:${_enclave_port},reuseaddr,fork,bind=${_enclave_bind_ip}" "TCP:127.0.0.1:${_enclave_port}" >/dev/null 2>&1 &
}
