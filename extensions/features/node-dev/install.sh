#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install nvm and Node.js global development packages
set -e

# Always install nvm for version management
export NVM_DIR="${NVM_DIR:-$HOME/.nvm}"
mkdir -p "$NVM_DIR"

base_node_bin="$(command -v node || true)"
base_node_version=""
if [ -n "$base_node_bin" ]; then
    base_node_version="$("$base_node_bin" --version 2>/dev/null || true)"
fi

if [ ! -s "$NVM_DIR/nvm.sh" ]; then
    echo "Installing nvm..."
    NVM_VERSION=$(curl -s https://api.github.com/repos/nvm-sh/nvm/releases/latest | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
    curl -o- "https://raw.githubusercontent.com/nvm-sh/nvm/${NVM_VERSION}/install.sh" | bash
fi

if [ -s "$NVM_DIR/nvm.sh" ]; then
    . "$NVM_DIR/nvm.sh"
    if [ -n "$base_node_bin" ]; then
        echo "Detected existing Node.js at $base_node_bin ${base_node_version}; skipping nvm install --lts"
        nvm alias default system >/dev/null 2>&1 || true
    else
        nvm install --lts
        current_version=$(nvm current)
        if [ "$current_version" != "none" ] && [ "$current_version" != "system" ] && [ "$current_version" != "N/A" ]; then
            nvm alias default "$current_version"
        fi
    fi

    # Guarantee the default alias resolves to a concrete nvm-managed version so
    # the runtime entrypoint's `nvm use default` succeeds. The branches above can
    # leave it unresolvable: `nvm current` can report `none`/`N/A` after install,
    # setting no alias at all. (On Node base images the `default -> system` alias
    # above resolves at build time, so this guard is a no-op there.) Otherwise,
    # pin default to the newest installed nvm version.
    if ! nvm use default >/dev/null 2>&1; then
        resolved_default="$(nvm version node 2>/dev/null || true)"
        if [ -n "$resolved_default" ] && [ "$resolved_default" != "N/A" ]; then
            nvm alias default "$resolved_default" >/dev/null 2>&1 || true
        fi
    fi
fi

# Install global dev packages with the active user-facing npm.
if ! command -v npm >/dev/null 2>&1; then
    echo "npm is not available after nvm setup" >&2
    exit 1
fi
# Install global dev packages into ~/.local (user-writable and universally on
# PATH) via a COMMAND-SCOPED npm_config_prefix. We deliberately avoid
# `npm config set prefix`, which persists `prefix=` into ~/.npmrc: nvm treats a
# prefix/globalconfig in npmrc as incompatible and aborts `nvm use` with exit 11
# on every subsequent `nvm.sh` source (later per-feature build layers and the
# runtime entrypoint). Scoping the prefix to just this command keeps ~/.npmrc
# clean so nvm stays usable. These packages are convenience tools; keep a usable
# Node.js runtime build-fatal, but do not fail the image for a registry blip here.
if ! npm_config_prefix="$HOME/.local" npm install -g --no-audit --no-fund --progress=false \
    typescript @types/node ts-node eslint prettier nodemon yarn pnpm; then
    echo "Warning: failed to install optional Node.js global development packages" >&2
fi

# Snapshot the default version so it can seed an empty cache mount at runtime.
# Recreate the snapshot instead of copying into an existing destination; this
# keeps re-runs from creating nested versions-default/versions trees.
if [ -d "$NVM_DIR/versions/node" ]; then
    rm -rf "$NVM_DIR/versions-default"
    cp -a "$NVM_DIR/versions" "$NVM_DIR/versions-default"
fi

if [ -z "$base_node_bin" ]; then
    node_snapshot_found=0
    for node_snapshot in "$NVM_DIR"/versions-default/node/*/bin/node; do
        if [ -x "$node_snapshot" ]; then
            node_snapshot_found=1
            break
        fi
    done
    if [ "$node_snapshot_found" != "1" ]; then
        echo "Node.js installation did not produce a runtime snapshot" >&2
        exit 1
    fi
    unset node_snapshot node_snapshot_found
fi

echo "Node.js development tools installed (nvm + global packages)"
