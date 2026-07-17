#!/bin/sh
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Shared auth reconciliation helpers used by the runtime entrypoint and helper containers.

# The caller may set enclave_auth_chown before invoking copy helpers.
: "${enclave_auth_chown:=}"

enclave_is_uint() {
    case "$1" in
        ''|*[!0-9]*) return 1 ;;
        *) return 0 ;;
    esac
}

enclave_copy_auth_file() {
    src_file=$1
    dst_file=$2
    [ -s "$src_file" ] || return 0
    mkdir -p "$(dirname "$dst_file")"
    tmp_file="${dst_file}.tmp.$$"
    rm -f "$tmp_file"
    if cp -p "$src_file" "$tmp_file" 2>/dev/null || cp "$src_file" "$tmp_file"; then
        chmod 600 "$tmp_file" 2>/dev/null || true
        if [ -n "$enclave_auth_chown" ]; then
            chown "$enclave_auth_chown" "$tmp_file" 2>/dev/null || true
        fi
        mv -f "$tmp_file" "$dst_file"
    else
        rm -f "$tmp_file"
        return 1
    fi
}

enclave_claude_expires_at() {
    file=$1
    [ -s "$file" ] || return 0
    if ! command -v jq >/dev/null 2>&1; then
        echo "jq is required to reconcile Claude credentials" >&2
        return 2
    fi
    value=$(jq -r '(.claudeAiOauth.expiresAt // empty)' "$file" 2>/dev/null || true)
    if enclave_is_uint "$value"; then
        printf '%s\n' "$value"
    fi
}

enclave_warn_claude_credentials_drift() {
    src_file=$1
    if [ -s "$src_file" ] && [ ! -L "$src_file" ]; then
        # This warning is intended to be user-visible during entrypoint startup.
        # Host-side finalization reuses this helper in a non-attached container,
        # so its stderr may be discarded; unresolved drift is warned again on the
        # next startup.
        echo "Warning: Claude wrote a real .credentials.json in its config dir; shared secure storage may not be active." >&2
    fi
}

enclave_sync_claude_credentials() {
    src_file=$1
    dst_file=$2
    # The healthy path is a symlink to the shared auth store. In helper
    # containers that absolute symlink target is usually not mounted at the same
    # path, so symlinks are a cheap no-op.
    if [ -L "$src_file" ]; then
        return 0
    fi
    if [ ! -s "$src_file" ]; then
        return 0
    fi
    enclave_warn_claude_credentials_drift "$src_file"
    if [ ! -s "$dst_file" ]; then
        enclave_copy_auth_file "$src_file" "$dst_file"
        return 0
    fi
    src_exp=$(enclave_claude_expires_at "$src_file") || return $?
    dst_exp=$(enclave_claude_expires_at "$dst_file") || return $?
    if enclave_is_uint "$src_exp" && { ! enclave_is_uint "$dst_exp" || [ "$src_exp" -gt "$dst_exp" ]; }; then
        enclave_copy_auth_file "$src_file" "$dst_file"
    fi
}

enclave_sync_additive_auth_file() {
    src_file=$1
    dst_file=$2
    if [ -s "$src_file" ] && [ ! -s "$dst_file" ]; then
        enclave_copy_auth_file "$src_file" "$dst_file"
    fi
}

enclave_sync_shared_auth() {
    tool=$1
    config_root=$2
    auth_root=$3
    enclave_auth_chown=$4
    link_config=$5
    shift 5

    mkdir -p "$auth_root" "$config_root"
    for auth_file in "$@"; do
        [ -n "$auth_file" ] || continue
        auth_subdir=$(dirname "$auth_file")
        if [ "$auth_subdir" != "." ]; then
            mkdir -p "$auth_root/$auth_subdir" "$config_root/$auth_subdir"
        fi
        auth_path="$auth_root/$auth_file"
        config_path="$config_root/$auth_file"
        if [ "$tool" = "claude" ] && [ "$auth_file" = ".credentials.json" ]; then
            enclave_sync_claude_credentials "$config_path" "$auth_path"
        else
            enclave_sync_additive_auth_file "$config_path" "$auth_path"
        fi
        if [ "$link_config" = "1" ]; then
            if [ -L "$config_path" ] && [ ! -s "$auth_path" ]; then
                rm -f "$config_path"
            fi
            if [ -s "$auth_path" ]; then
                ln -sf "$auth_path" "$config_path"
            fi
        fi
    done
}
