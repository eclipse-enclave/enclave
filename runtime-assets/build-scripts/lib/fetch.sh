#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Network helpers for build-time downloads.
#
# Build-time fetches go through enclave_curl (one HTTP transfer with connect
# and stall timeouts) or enclave_retry (any command whose stderr identifies a
# transient network error). Both read their tunables from the environment so a
# build on a poor connection can raise them; the CLI forwards ENCLAVE_NET_*
# variables from the host environment as build args. The install runners
# export the helpers into extension install.sh processes.
#
# This file stands on its own: common.sh sources it, and the Dockerfile copies
# it into the image by itself ahead of the system and tool-base package
# installs, which run before the rest of the build scripts exist there. Keep it
# free of dependencies on common.sh; every change to it rebuilds those apt
# layers, so the rarely changing helpers live here and nothing else does.

: "${ENCLAVE_NET_RETRIES:=5}"
: "${ENCLAVE_NET_RETRY_DELAY_SECONDS:=5}"
: "${ENCLAVE_NET_CONNECT_TIMEOUT_SECONDS:=20}"
: "${ENCLAVE_NET_STALL_TIMEOUT_SECONDS:=60}"
: "${ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS:=1800}"
: "${ENCLAVE_NET_STALL_SPEED_BYTES:=1024}"
: "${ENCLAVE_NET_PROGRESS_INTERVAL_SECONDS:=30}"

enclave_net_settings_valid() {
    local name=""
    for name in ENCLAVE_NET_RETRIES ENCLAVE_NET_RETRY_DELAY_SECONDS \
        ENCLAVE_NET_CONNECT_TIMEOUT_SECONDS ENCLAVE_NET_STALL_TIMEOUT_SECONDS \
        ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS ENCLAVE_NET_STALL_SPEED_BYTES \
        ENCLAVE_NET_PROGRESS_INTERVAL_SECONDS; do
        case "${!name}" in
            '' | *[!0-9]*)
                echo "${name} must be a non-negative integer, got: ${!name}" >&2
                return 1
                ;;
        esac
    done
    if [ "$ENCLAVE_NET_RETRIES" -lt 1 ]; then
        echo "ENCLAVE_NET_RETRIES must be >= 1, got: ${ENCLAVE_NET_RETRIES}" >&2
        return 1
    fi
}

# enclave_is_transient_network_error <file> reports whether captured command
# output looks like a network failure worth retrying: name resolution, connect
# and read timeouts, resets, stalled transfers, and gateway-side 5xx responses.
# A 404 or a checksum mismatch is not on the list, so wrong URLs fail fast.
enclave_is_transient_network_error() {
    grep -Eiq \
        -e 'Temporary failure (resolving|in name resolution)' \
        -e 'Could not resolve' \
        -e 'no such host' \
        -e 'Name or service not known' \
        -e 'i/o timeout' \
        -e 'TLS handshake timeout' \
        -e 'timed out' \
        -e 'network is unreachable' \
        -e 'connection reset by peer' \
        -e 'connection refused' \
        -e 'proxyconnect tcp' \
        -e 'dial tcp' \
        -e 'unexpected EOF' \
        -e 'context deadline exceeded' \
        -e 'Failed to fetch' \
        -e 'Could not connect to' \
        -e 'Unable to connect to' \
        -e 'transfer closed' \
        -e '(Recv|Send) failure' \
        -e 'Hash Sum mismatch' \
        -e 'E(CONNRESET|TIMEDOUT|NOTFOUND|AI_AGAIN|CONNREFUSED|HOSTUNREACH|NETUNREACH)' \
        -e 'curl: \(([67]|1[68]|2[38]|35|5[2567]|92)\)' \
        -e 'returned error: (408|425|429|5[0-9][0-9])' \
        "$1"
}

# enclave_retry <label> [--] <cmd> [args...]
# Runs cmd, retrying with a growing delay while its stderr shows a transient
# network error or the attempt hits ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS (0
# disables the per-attempt timeout). stdout streams through untouched; stderr
# is captured per attempt and replayed on failure. Returns the last exit status.
enclave_retry() {
    local label="$1"
    shift
    if [ "${1:-}" = "--" ]; then
        shift
    fi
    if [ "$#" -lt 1 ]; then
        echo "enclave_retry: missing command for ${label}" >&2
        return 2
    fi
    enclave_net_settings_valid || return 2

    local attempt=1
    local status=0
    local delay=0
    local errlog=""
    # Dynamically scoped: a wrapped shell function such as enclave_curl_attempt
    # reads it to behave differently on retries.
    local ENCLAVE_RETRY_ATTEMPT=1
    errlog="$(mktemp)"

    # timeout(1) only runs executables; shell functions and builtins run
    # without the per-attempt deadline.
    local -a runner=()
    if [ "$ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS" -gt 0 ] && [ "$(type -t "$1")" = "file" ] &&
        command -v timeout >/dev/null 2>&1; then
        runner=(timeout "$ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS")
    fi

    while :; do
        status=0
        ENCLAVE_RETRY_ATTEMPT="$attempt"
        "${runner[@]}" "$@" 2>>"$errlog" || status=$?
        if [ "$status" -eq 0 ]; then
            cat "$errlog" >&2
            rm -f "$errlog"
            return 0
        fi

        cat "$errlog" >&2
        if [ "$status" -eq 124 ]; then
            echo "${label}: attempt ${attempt} timed out after ${ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS}s" >&2
        fi
        if [ "$attempt" -ge "$ENCLAVE_NET_RETRIES" ]; then
            echo "${label}: failed after ${attempt} attempt(s)" >&2
            rm -f "$errlog"
            return "$status"
        fi
        if [ "$status" -ne 124 ] && ! enclave_is_transient_network_error "$errlog"; then
            echo "${label}: failed with a non-retryable error" >&2
            rm -f "$errlog"
            return "$status"
        fi

        delay=$((ENCLAVE_NET_RETRY_DELAY_SECONDS * attempt))
        echo "${label}: transient network failure; retrying (attempt $((attempt + 1))/${ENCLAVE_NET_RETRIES}) in ${delay}s" >&2
        sleep "$delay"
        : >"$errlog"
        attempt=$((attempt + 1))
    done
}

# enclave_curl [curl args...]
# curl with --fail, connect and stall timeouts, wrapped in enclave_retry. A
# transfer delivering under ENCLAVE_NET_STALL_SPEED_BYTES per second for
# ENCLAVE_NET_STALL_TIMEOUT_SECONDS is aborted so a silent peer becomes a
# retryable failure instead of a hang. Retries of a -o download resume the
# partial file where the server allows it, and a heartbeat reports the bytes
# received every ENCLAVE_NET_PROGRESS_INTERVAL_SECONDS while a download runs,
# so a slow link can be told apart from a dead one. Without -o/-O the body is
# buffered per attempt and written to stdout only on success, so
# `$(enclave_curl url)` never sees a partial body from a failed try.
enclave_curl() {
    enclave_net_settings_valid || return 2
    local label="curl"
    local output=""
    local capture=1
    local arg=""
    local prev=""
    for arg in "$@"; do
        case "$arg" in
            http://* | https://*) label="curl ${arg}" ;;
        esac
        case "$prev" in
            -o | --output) output="$arg" ;;
        esac
        case "$arg" in
            -o | --output) capture=0 ;;
            -O | --remote-name) capture=0 ;;
            --output=*) output="${arg#--output=}"; capture=0 ;;
            -o?*) output="${arg#-o}"; capture=0 ;;
        esac
        prev="$arg"
    done

    local body=""
    local status=0
    if [ "$capture" -eq 1 ]; then
        body="$(mktemp)"
        output="$body"
    fi

    local heartbeat_pid=""
    if [ -n "$output" ] && [ "$ENCLAVE_NET_PROGRESS_INTERVAL_SECONDS" -gt 0 ]; then
        enclave_download_heartbeat "$label" "$output" &
        heartbeat_pid=$!
    fi

    if [ "$capture" -eq 1 ]; then
        enclave_retry "$label" -- enclave_curl_attempt "$output" -o "$output" "$@" || status=$?
    else
        enclave_retry "$label" -- enclave_curl_attempt "$output" "$@" || status=$?
    fi

    if [ -n "$heartbeat_pid" ]; then
        kill "$heartbeat_pid" 2>/dev/null || true
        wait "$heartbeat_pid" 2>/dev/null || true
    fi
    if [ -n "$output" ]; then
        rm -f "${output}.enclave-headers"
    fi
    if [ "$capture" -eq 1 ]; then
        if [ "$status" -eq 0 ]; then
            cat "$body"
        fi
        rm -f "$body"
    fi
    return "$status"
}

# enclave_curl_attempt <output-file-or-empty> [curl args...]
# One curl run with the shared flags; enclave_curl wraps it in enclave_retry.
# From the second attempt on, a non-empty output file is resumed with
# --continue-at so a large archive does not restart from zero after a hiccup.
# When the server cannot resume (curl exit 33, or HTTP 416 for a range it
# rejects) the partial file is discarded and the attempt downloads from zero.
enclave_curl_attempt() {
    local output="$1"
    shift
    local -a curl_args=(
        --fail --silent --show-error
        --connect-timeout "$ENCLAVE_NET_CONNECT_TIMEOUT_SECONDS"
        --speed-limit "$ENCLAVE_NET_STALL_SPEED_BYTES"
        --speed-time "$ENCLAVE_NET_STALL_TIMEOUT_SECONDS"
    )
    if [ -n "$output" ]; then
        # The response headers let the heartbeat report a percentage.
        curl_args+=(--dump-header "${output}.enclave-headers")
    fi
    # As in enclave_retry, timeout(1) only wraps an executable curl (tests
    # substitute a shell function).
    local -a runner=()
    if [ "$ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS" -gt 0 ] && [ "$(type -t curl)" = "file" ] &&
        command -v timeout >/dev/null 2>&1; then
        runner=(timeout "$ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS")
    fi

    if [ -n "$output" ] && [ "${ENCLAVE_RETRY_ATTEMPT:-1}" -gt 1 ] && [ -s "$output" ]; then
        local status=0
        local errlog=""
        errlog="$(mktemp)"
        echo "resuming download at $(($(stat -c %s "$output" 2>/dev/null || echo 0) / 1024)) KB" >&2
        "${runner[@]}" curl "${curl_args[@]}" --continue-at - "$@" 2>"$errlog" || status=$?
        cat "$errlog" >&2
        if [ "$status" -eq 0 ]; then
            rm -f "$errlog"
            return 0
        fi
        if [ "$status" -eq 33 ] || grep -q 'returned error: 416' "$errlog"; then
            echo "server does not support resuming; restarting the download from zero" >&2
            rm -f "$errlog" "$output"
        else
            rm -f "$errlog"
            return "$status"
        fi
    fi
    "${runner[@]}" curl "${curl_args[@]}" "$@"
}

# enclave_download_heartbeat <label> <file>
# Every ENCLAVE_NET_PROGRESS_INTERVAL_SECONDS, reports how much of a download
# has arrived, the current rate, and (when the response headers in
# <file>.enclave-headers carry a length) the percentage and time remaining.
# Runs in the background from enclave_curl until killed. Prints whole lines
# rather than a redrawing progress bar because RUN-step output inside an image
# build is line-oriented and would garble carriage returns.
enclave_download_heartbeat() {
    local label="$1"
    local file="$2"
    local size=0
    local previous=0
    local total=0
    local rate=0
    local remaining=""
    while :; do
        sleep "$ENCLAVE_NET_PROGRESS_INTERVAL_SECONDS"
        size="$(stat -c %s "$file" 2>/dev/null || echo 0)"
        rate=$(((size - previous) / ENCLAVE_NET_PROGRESS_INTERVAL_SECONDS))
        [ "$rate" -lt 0 ] && rate=0
        previous="$size"
        total="$(enclave_download_total "${file}.enclave-headers")"
        if [ "$total" -gt 0 ] && [ "$size" -le "$total" ]; then
            remaining=""
            if [ "$rate" -gt 0 ]; then
                remaining=", about $(enclave_format_duration $(((total - size) / rate))) left"
            fi
            echo "${label}: $(enclave_format_bytes "$size") of $(enclave_format_bytes "$total") ($((size * 100 / total))%), $(enclave_format_bytes "$rate")/s${remaining}" >&2
        else
            echo "${label}: $(enclave_format_bytes "$size") received, $(enclave_format_bytes "$rate")/s" >&2
        fi
    done
}

# enclave_download_total <header-dump> echoes the expected final size of a
# download from its response headers, or 0 when unknown. A Content-Range from
# a resumed transfer carries the full size; otherwise the last Content-Length
# (the final response after redirects) is used.
enclave_download_total() {
    local headers="$1"
    local value=""
    [ -r "$headers" ] || { echo 0; return; }
    value="$(tr -d '\r' < "$headers" | grep -i '^content-range:' | tail -n 1 | sed -n 's|.*/\([0-9][0-9]*\)$|\1|p')"
    if [ -z "$value" ]; then
        value="$(tr -d '\r' < "$headers" | grep -i '^content-length:' | tail -n 1 | tr -dc '0-9')"
    fi
    echo "${value:-0}"
}

enclave_format_bytes() {
    local bytes="$1"
    if [ "$bytes" -ge 1048576 ]; then
        echo "$((bytes / 1048576)).$(((bytes % 1048576) * 10 / 1048576)) MB"
    elif [ "$bytes" -ge 1024 ]; then
        echo "$((bytes / 1024)) KB"
    else
        echo "${bytes} B"
    fi
}

enclave_format_duration() {
    local seconds="$1"
    if [ "$seconds" -ge 3600 ]; then
        echo "$((seconds / 3600))h$(((seconds % 3600) / 60))m"
    elif [ "$seconds" -ge 60 ]; then
        echo "$((seconds / 60))m$((seconds % 60))s"
    else
        echo "${seconds}s"
    fi
}

# enclave_apt_get <apt-get args...>
# apt-get under enclave_retry, with its download progress reported the way
# enclave_curl reports its own. In a build step apt-get prints one line per
# package as each download starts and nothing while a large one transfers, so
# the machine-readable APT::Status-Fd channel is read instead and summarised
# every ENCLAVE_NET_PROGRESS_INTERVAL_SECONDS as whole lines.
enclave_apt_get() {
    enclave_net_settings_valid || return 2
    local label="apt-get ${1:-}"
    local status=0
    if [ "$ENCLAVE_NET_PROGRESS_INTERVAL_SECONDS" -le 0 ]; then
        enclave_retry "$label" -- apt-get "$@"
        return
    fi

    local fifo=""
    fifo="$(mktemp -u)"
    mkfifo "$fifo"
    enclave_apt_progress_reporter "$label" < "$fifo" &
    local reporter_pid=$!
    enclave_retry "$label" -- apt-get -o APT::Status-Fd=3 "$@" 3>"$fifo" || status=$?
    wait "$reporter_pid" 2>/dev/null || true
    rm -f "$fifo"
    return "$status"
}

# enclave_apt_progress_reporter <label>
# Reads APT::Status-Fd lines (dlstatus:<file>:<percent>:<description>) on
# stdin and prints the overall download percentage at most once per
# ENCLAVE_NET_PROGRESS_INTERVAL_SECONDS. Package unpack and setup steps are
# already visible in apt-get's regular output and are not repeated.
enclave_apt_progress_reporter() {
    local label="$1"
    local kind="" percent="" description=""
    local last=0
    local now=0
    while IFS=: read -r kind _ percent description; do
        [ "$kind" = "dlstatus" ] || continue
        now="$(date +%s)"
        if [ $((now - last)) -lt "$ENCLAVE_NET_PROGRESS_INTERVAL_SECONDS" ]; then
            continue
        fi
        last="$now"
        echo "${label}: downloading ${percent%%.*}% (${description})" >&2
    done
}

# enclave_export_net_helpers makes the network helpers and their settings
# available to child bash processes, which is how extension install.sh scripts
# get them from the install runners without sourcing this file.
enclave_export_net_helpers() {
    export ENCLAVE_NET_RETRIES ENCLAVE_NET_RETRY_DELAY_SECONDS \
        ENCLAVE_NET_CONNECT_TIMEOUT_SECONDS ENCLAVE_NET_STALL_TIMEOUT_SECONDS \
        ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS ENCLAVE_NET_STALL_SPEED_BYTES \
        ENCLAVE_NET_PROGRESS_INTERVAL_SECONDS
    export -f enclave_net_settings_valid enclave_is_transient_network_error \
        enclave_retry enclave_curl enclave_curl_attempt enclave_download_heartbeat \
        enclave_download_total enclave_format_bytes enclave_format_duration \
        enclave_apt_get enclave_apt_progress_reporter
}
