#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

: "${ENCLAVE_EXTENSIONS_ROOT:=/opt/enclave/extensions}"
: "${ENCLAVE_FEATURES_DIR:=${ENCLAVE_EXTENSIONS_ROOT}/features}"
: "${ENCLAVE_TOOLS_DIR:=${ENCLAVE_EXTENSIONS_ROOT}/tools}"
: "${ENCLAVE_AGENT_NODE_DIR:=/opt/enclave/node}"
: "${ENCLAVE_BUILD_SCRIPTS_DIR:=/opt/enclave/build-scripts}"
: "${ENCLAVE_TEMPLATES_DIR:=/usr/local/share/enclave/templates}"
: "${ENCLAVE_INSTALLED_TOOLS_FILE:=/tmp/installed-tools.txt}"

enclave_require_command() {
    local cmd="$1"
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "missing required command: $cmd" >&2
        exit 1
    fi
}

enclave_require_dir() {
    local dir="$1"
    if [ ! -d "$dir" ]; then
        echo "missing required directory: $dir" >&2
        exit 1
    fi
}

enclave_load_nvm_no_use() {
    local nvm_dir="${1:-${NVM_DIR:-$HOME/.nvm}}"
    if [ ! -s "$nvm_dir/nvm.sh" ]; then
        return 1
    fi
    export NVM_DIR="$nvm_dir"
    # Load nvm WITHOUT auto-activating: a bare source implicitly runs
    # `nvm use default`, which can return non-zero (exit 11 on an ~/.npmrc
    # prefix conflict, exit 3 on a version cache miss) and abort callers running
    # under `set -e`. --no-use loads functions only; callers can activate the
    # desired version explicitly and non-fatally.
    # shellcheck source=/dev/null
    . "$NVM_DIR/nvm.sh" --no-use || true
}

# enclave_ext_spec echoes the path to an extension's spec manifest
# (spec.yaml, falling back to spec.json) inside the given extension directory,
# or nothing when neither exists. The build reads extension metadata straight
# from the frozen spec.yaml grammar via yq.
enclave_ext_spec() {
    local dir="$1"
    if [ -f "$dir/spec.yaml" ]; then
        echo "$dir/spec.yaml"
    elif [ -f "$dir/spec.json" ]; then
        echo "$dir/spec.json"
    fi
}

# enclave_spec_read evaluates a yq expression against a spec manifest and
# echoes the result. When the manifest is missing it echoes the caller-supplied
# fallback so callers get the same default the old jq null-checks provided.
enclave_spec_read() {
    local spec="$1"
    local expr="$2"
    local fallback="${3-}"
    if [ -z "$spec" ] || [ ! -f "$spec" ]; then
        printf '%s' "$fallback"
        return 0
    fi
    yq "$expr" "$spec"
}

# enclave_copy_home_files bakes an extension's files/home tree into $HOME,
# preserving mode and overwriting existing files (kit wins). The trailing "/."
# copies the directory contents (including dotfiles) and merges subdirectories
# into any existing $HOME layout. No-op when files/home is absent.
#
# When ENCLAVE_HOME_FILES_SRC is set (build time), read files/home from a
# pristine, agent-owned copy of the extension tree rooted there instead of the
# working copy under /opt/enclave/extensions, which was chmod'd a+rX for agent
# traversal and would widen restrictive modes (e.g. 0640 -> 0644). Derives the
# kit's kind+name from ext_dir (.../extensions/{features,tools}/<name>). Falls
# back to the working copy when the pristine tree or env var is absent, so plain
# `docker build` and the unit test still work.
enclave_copy_home_files() {
    local ext_dir="$1"
    local home_src="$ext_dir/files/home"
    if [ -n "${ENCLAVE_HOME_FILES_SRC:-}" ]; then
        local name kind pristine
        name="$(basename "$ext_dir")"
        kind="$(basename "$(dirname "$ext_dir")")"
        pristine="$ENCLAVE_HOME_FILES_SRC/$kind/$name/files/home"
        [ -d "$pristine" ] && home_src="$pristine"
    fi
    [ -d "$home_src" ] || return 0
    mkdir -p "$HOME"
    cp -R --preserve=mode "$home_src/." "$HOME/"
}

# enclave_is_root_user reports whether a spec command's user field is root.
enclave_is_root_user() {
    case "${1:-}" in
        0 | root) return 0 ;;
        *) return 1 ;;
    esac
}

# enclave_run_install_command <user> <cmd> [args...]
# Runs a commands.install argv. When the current process is root AND the
# command targets a non-root user, drop privileges via
# ${ENCLAVE_SUDO:-sudo} -u ${ENCLAVE_AGENT_USER:-agent}. Otherwise run
# directly (agent-phase RUNs already execute as the sandbox user). Returns the
# command's exit status so callers can honor failOnInstallError. No-op with no
# command argv.
enclave_run_install_command() {
    local user="$1"
    shift
    [ "$#" -ge 1 ] || return 0
    if [ "$(id -u)" -eq 0 ] && ! enclave_is_root_user "$user"; then
        "${ENCLAVE_SUDO:-sudo}" -u "${ENCLAVE_AGENT_USER:-agent}" -- "$@"
    else
        "$@"
    fi
}

enclave_word_list_contains() {
    local haystack="${1:-}"
    local needle="${2:-}"
    local token=""
    for token in $haystack; do
        if [ "$token" = "$needle" ]; then
            return 0
        fi
    done
    return 1
}

enclave_feature_is_enabled() {
    local spec="$1"
    local feature_name="$2"
    local selection="${3-default}"

    if [ -z "$selection" ]; then
        return 1
    fi

    # Explicitly listed by name — always enabled
    if enclave_word_list_contains "$selection" "$feature_name"; then
        return 0
    fi

    # "all" keyword — every feature regardless of defaultEnabled
    if [ "$selection" = "all" ] || enclave_word_list_contains "$selection" "all"; then
        return 0
    fi

    # "default" keyword — only features with defaultEnabled true/omitted.
    # `!= false` treats an absent (null) key as enabled while honoring an
    # explicit `defaultEnabled: false`, matching the retired jq null-check.
    if [ "$selection" = "default" ] || enclave_word_list_contains "$selection" "default"; then
        [ "$(yq '.defaultEnabled != false' "$spec")" = "true" ]
        return
    fi

    return 1
}

enclave_tool_is_enabled() {
    local spec="$1"
    local tool_name="$2"
    local selection="${3-all}"

    if [ -z "$selection" ]; then
        return 1
    fi

    # "all" keyword — default-included tools only
    if [ "$selection" = "all" ]; then
        if [ -z "$spec" ] || [ ! -f "$spec" ]; then
            return 0
        fi
        [ "$(yq '.defaultIncluded != false' "$spec")" = "true" ]
        return
    fi

    enclave_word_list_contains "$selection" "$tool_name"
}

enclave_list_enabled_features() {
    local selection="${1-default}"
    local ext=""
    local spec=""
    local name=""
    local priority=""

    enclave_require_command yq

    for ext in "$ENCLAVE_FEATURES_DIR"/*/; do
        [ -d "$ext" ] || continue
        ext="${ext%/}"
        spec="$(enclave_ext_spec "$ext")"
        [ -n "$spec" ] || continue

        name="$(basename "$ext")"
        if ! enclave_feature_is_enabled "$spec" "$name" "$selection"; then
            continue
        fi

        priority="$(yq '.priority // 100' "$spec")"
        printf '%s\t%s\t%s\n' "$priority" "$name" "$ext"
    done
}

enclave_list_feature_installers() {
    local selection="${1-default}"
    local phase="${2:-user}"
    local priority=""
    local name=""
    local ext=""
    local spec=""
    local needs_root=""
    local script=""

    while IFS=$'\t' read -r priority name ext; do
        spec="$(enclave_ext_spec "$ext")"
        needs_root="$(enclave_spec_read "$spec" '.needsRoot // false' false)"

        case "$phase" in
            root)
                [ "$needs_root" = "true" ] || continue
                ;;
            user)
                [ "$needs_root" = "true" ] && continue
                ;;
            *)
                echo "invalid feature install phase: $phase" >&2
                exit 1
                ;;
        esac

        script="$ext/install.sh"
        [ -x "$script" ] || continue
        printf '%s\t%s\t%s\n' "$priority" "$name" "$script"
    done < <(enclave_list_enabled_features "$selection")
}

# ---------------------------------------------------------------------------
# Network helpers for build-time downloads.
#
# Build-time fetches go through enclave_curl (one HTTP transfer with connect
# and stall timeouts) or enclave_retry (any command whose stderr identifies a
# transient network error). Both read their tunables from the environment so a
# build on a poor connection can raise them; the CLI forwards ENCLAVE_NET_*
# variables from the host environment as build args. The install runners
# export the helpers into extension install.sh processes.

: "${ENCLAVE_NET_RETRIES:=5}"
: "${ENCLAVE_NET_RETRY_DELAY_SECONDS:=5}"
: "${ENCLAVE_NET_CONNECT_TIMEOUT_SECONDS:=20}"
: "${ENCLAVE_NET_STALL_TIMEOUT_SECONDS:=60}"
: "${ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS:=1800}"

enclave_net_settings_valid() {
    local name=""
    for name in ENCLAVE_NET_RETRIES ENCLAVE_NET_RETRY_DELAY_SECONDS \
        ENCLAVE_NET_CONNECT_TIMEOUT_SECONDS ENCLAVE_NET_STALL_TIMEOUT_SECONDS \
        ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS; do
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
# transfer delivering under 1 KB/s for ENCLAVE_NET_STALL_TIMEOUT_SECONDS is
# aborted so a silent peer becomes a retryable failure instead of a hang.
# Without -o/-O the body is buffered per attempt and written to stdout only on
# success, so `$(enclave_curl url)` never sees a partial body from a failed try.
enclave_curl() {
    local label="curl"
    local capture=1
    local arg=""
    for arg in "$@"; do
        case "$arg" in
            http://* | https://*) label="curl ${arg}" ;;
            -o | --output | -O | --remote-name | -o* | --output=*) capture=0 ;;
        esac
    done

    local -a curl_args=(
        --fail --silent --show-error
        --connect-timeout "$ENCLAVE_NET_CONNECT_TIMEOUT_SECONDS"
        --speed-limit 1024 --speed-time "$ENCLAVE_NET_STALL_TIMEOUT_SECONDS"
    )
    if [ "$capture" -eq 0 ]; then
        enclave_retry "$label" -- curl "${curl_args[@]}" "$@"
        return
    fi

    local body=""
    local status=0
    body="$(mktemp)"
    enclave_retry "$label" -- curl "${curl_args[@]}" -o "$body" "$@" || status=$?
    if [ "$status" -eq 0 ]; then
        cat "$body"
    fi
    rm -f "$body"
    return "$status"
}

# enclave_export_net_helpers makes the network helpers and their settings
# available to child bash processes, which is how extension install.sh scripts
# get them from the install runners without sourcing this file.
enclave_export_net_helpers() {
    export ENCLAVE_NET_RETRIES ENCLAVE_NET_RETRY_DELAY_SECONDS \
        ENCLAVE_NET_CONNECT_TIMEOUT_SECONDS ENCLAVE_NET_STALL_TIMEOUT_SECONDS \
        ENCLAVE_NET_ATTEMPT_TIMEOUT_SECONDS
    export -f enclave_net_settings_valid enclave_is_transient_network_error \
        enclave_retry enclave_curl
}
