# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# shellcheck shell=bash
# Node.js version selection precedence:
#   project .nvmrc > nvm default

if [ -s "$HOME/.nvm/nvm.sh" ] && ! command -v nvm >/dev/null 2>&1; then
    if command -v enclave_load_nvm_no_use >/dev/null 2>&1; then
        enclave_load_nvm_no_use "$HOME/.nvm"
    else
        export NVM_DIR="$HOME/.nvm"
        # shellcheck disable=SC1091 # Optional runtime dependency.
        . "$NVM_DIR/nvm.sh" --no-use || true
    fi
fi

_enclave_node_private_bin_dir() {
    local _enclave_dir
    _enclave_dir="${ENCLAVE_AGENT_NODE_DIR:-/opt/enclave/node}/bin"
    readlink -f "$_enclave_dir" 2>/dev/null || printf '%s\n' "$_enclave_dir"
}

_enclave_current_node_dir() {
    local _enclave_node_bin _enclave_node_real _enclave_private_dir

    _enclave_node_bin="$(command -v node || true)"
    [ -n "$_enclave_node_bin" ] || return 1

    _enclave_node_real="$(readlink -f "$_enclave_node_bin" 2>/dev/null || printf '%s\n' "$_enclave_node_bin")"
    _enclave_private_dir="$(_enclave_node_private_bin_dir)"
    case "$_enclave_node_real" in
        "$_enclave_private_dir"/*) return 1 ;;
    esac

    dirname "$_enclave_node_real"
}

_enclave_normalized_node_version() {
    local _enclave_version
    _enclave_version="$1"
    if [ "$_enclave_version" = "default" ] && command -v nvm >/dev/null 2>&1; then
        _enclave_version="$(nvm version default 2>/dev/null || true)"
    fi

    case "$_enclave_version" in
        ""|N/A|none|system) return 1 ;;
        v*) printf '%s\n' "$_enclave_version" ;;
        *) printf 'v%s\n' "$_enclave_version" ;;
    esac
}

_enclave_versions_default_node_dir() {
    local _enclave_preferred _enclave_version _enclave_dir
    _enclave_preferred="${1:-}"

    if _enclave_version="$(_enclave_normalized_node_version "$_enclave_preferred")"; then
        _enclave_dir="$HOME/.nvm/versions-default/node/$_enclave_version/bin"
        [ -x "$_enclave_dir/node" ] && { printf '%s\n' "$_enclave_dir"; return 0; }
    fi

    if _enclave_version="$(_enclave_normalized_node_version default)"; then
        _enclave_dir="$HOME/.nvm/versions-default/node/$_enclave_version/bin"
        [ -x "$_enclave_dir/node" ] && { printf '%s\n' "$_enclave_dir"; return 0; }
    fi

    for _enclave_dir in "$HOME/.nvm/versions-default/node"/*/bin; do
        [ -x "$_enclave_dir/node" ] || continue
        printf '%s\n' "$_enclave_dir"
        return 0
    done

    return 1
}

_enclave_expose_node_bins() {
    local _enclave_preferred _enclave_node_dir _enclave_cmd _enclave_target _enclave_link _enclave_target_real _enclave_link_real
    _enclave_preferred="${1:-default}"

    _enclave_node_dir="$(_enclave_current_node_dir || true)"
    if [ -z "$_enclave_node_dir" ]; then
        _enclave_node_dir="$(_enclave_versions_default_node_dir "$_enclave_preferred" || true)"
    fi
    [ -n "$_enclave_node_dir" ] || return 0

    mkdir -p "$HOME/.local/bin"
    for _enclave_cmd in node npm npx corepack; do
        _enclave_target="$_enclave_node_dir/$_enclave_cmd"
        _enclave_link="$HOME/.local/bin/$_enclave_cmd"
        [ -x "$_enclave_target" ] || continue
        [ "$_enclave_target" != "$_enclave_link" ] || continue

        if [ -e "$_enclave_link" ] || [ -L "$_enclave_link" ]; then
            _enclave_target_real="$(readlink -f "$_enclave_target" 2>/dev/null || printf '%s\n' "$_enclave_target")"
            _enclave_link_real="$(readlink -f "$_enclave_link" 2>/dev/null || printf '%s\n' "$_enclave_link")"
            [ "$_enclave_target_real" != "$_enclave_link_real" ] || continue
        fi

        ln -sfn "$_enclave_target" "$_enclave_link"
    done
}

if ! command -v nvm >/dev/null 2>&1; then
    _enclave_expose_node_bins default
    unset -f _enclave_expose_node_bins _enclave_current_node_dir _enclave_versions_default_node_dir _enclave_normalized_node_version _enclave_node_private_bin_dir
    return 0
fi

# Seed cache mount with build-time default version.
# Copies missing versions from the image snapshot into the persistent cache
# so that image upgrades make the new default available immediately.
if [ -d "$HOME/.nvm/versions-default/node" ]; then
    mkdir -p "$HOME/.nvm/versions/node"
    for _enclave_ver in "$HOME/.nvm/versions-default/node"/*/; do
        [ -d "$_enclave_ver" ] || continue
        _enclave_base="$(basename "$_enclave_ver")"
        if [ ! -d "$HOME/.nvm/versions/node/$_enclave_base" ]; then
            cp -a "$_enclave_ver" "$HOME/.nvm/versions/node/$_enclave_base"
        fi
    done
    unset _enclave_ver _enclave_base
fi

_enclave_node_target=""

# Project .nvmrc selects the Node version.
if [ -n "$PROJECT_DIR" ] && [ -f "$PROJECT_DIR/.nvmrc" ]; then
    _enclave_node_target="$(tr -d '[:space:]' < "$PROJECT_DIR/.nvmrc")"
fi

if [ -n "$_enclave_node_target" ]; then
    if ! nvm use "$_enclave_node_target" >/dev/null 2>&1; then
        if nvm install "$_enclave_node_target" >/dev/null 2>&1; then
            nvm use "$_enclave_node_target" >/dev/null 2>&1
        else
            echo "Warning: failed to install Node.js $_enclave_node_target via nvm"
        fi
    fi
else
    nvm use default >/dev/null 2>&1 || true
fi

# Last-resort activation: if selection left no usable (non-private) Node active
# — e.g. an older image whose persistent ~/.nvm/versions volume holds versions
# but has no default alias and no build-time snapshot — activate the newest
# installed version so _enclave_expose_node_bins has a target to link.
if ! _enclave_current_node_dir >/dev/null 2>&1; then
    nvm use node >/dev/null 2>&1 || true
fi

_enclave_expose_node_bins "${_enclave_node_target:-default}"

unset -f _enclave_expose_node_bins _enclave_current_node_dir _enclave_versions_default_node_dir _enclave_normalized_node_version _enclave_node_private_bin_dir
unset _enclave_node_target
