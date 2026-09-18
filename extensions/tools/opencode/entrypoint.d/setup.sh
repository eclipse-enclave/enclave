# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# shellcheck shell=bash
# OpenCode extension setup
config_dir="$HOME/.config/opencode"
data_dir="$HOME/.local/share/opencode"
state_dir="$HOME/.local/state/opencode"
state_store_dir="$config_dir/state"

mkdir -p "$config_dir" "$state_store_dir" "$HOME/.local/share" "$HOME/.local/state"

# OpenCode creates its XDG data and state directories on every start, including
# the `opencode --version` probe baked into the image build. Both therefore
# exist as real directories before this script runs and must be migrated into
# the config store, or the tool keeps writing to container-local storage that
# is discarded with the container.
opencode_link_into_store() {
    local link=$1
    local store=$2
    local src rel dst

    if [ -L "$link" ] && [ "$(readlink "$link")" != "$store" ]; then
        rm -f "$link"
    fi

    if [ -d "$link" ] && [ ! -L "$link" ]; then
        find "$link" -mindepth 1 -print | while IFS= read -r src; do
            rel=${src#"$link"/}
            dst="$store/$rel"
            if [ -d "$src" ]; then
                mkdir -p "$dst"
                continue
            fi
            if [ ! -e "$dst" ]; then
                mkdir -p "$(dirname "$dst")"
                cp -p "$src" "$dst"
            fi
        done
        rm -rf "$link"
    fi

    if [ ! -e "$link" ]; then
        ln -s "$store" "$link"
    fi
}

opencode_link_into_store "$data_dir" "$config_dir"
opencode_link_into_store "$state_dir" "$state_store_dir"

auth_file="$config_dir/auth.json"
shared_auth_file="${ENCLAVE_AUTH_DIR:-}/auth.json"
if [ -n "${ENCLAVE_AUTH_DIR:-}" ]; then
    if [ -L "$auth_file" ] && [ "$(readlink "$auth_file")" != "$shared_auth_file" ]; then
        rm -f "$auth_file"
    fi

    if [ ! -L "$auth_file" ]; then
        if [ -e "$auth_file" ] || [ -e "$shared_auth_file" ]; then
            node - "$shared_auth_file" "$auth_file" <<'NODE'
const fs = require("fs")
const path = require("path")

const [sharedPath, configPath] = process.argv.slice(2)

const read = (file) => {
  try {
    const value = JSON.parse(fs.readFileSync(file, "utf8"))
    if (value && typeof value === "object" && !Array.isArray(value)) return value
  } catch {}
  return {}
}

const merged = {
  ...read(sharedPath),
  ...read(configPath),
}

if (Object.keys(merged).length > 0) {
  fs.mkdirSync(path.dirname(sharedPath), { recursive: true })
  fs.writeFileSync(sharedPath, JSON.stringify(merged, null, 2) + "\n", { mode: 0o600 })
}
NODE
        fi

        rm -f "$auth_file"
        ln -s "$shared_auth_file" "$auth_file"
    fi
fi
