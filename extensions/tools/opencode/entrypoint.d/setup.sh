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

mkdir -p "$config_dir" "$HOME/.local/share"

if [ -L "$data_dir" ] && [ "$(readlink "$data_dir")" != "$config_dir" ]; then
    rm -f "$data_dir"
fi

if [ -d "$data_dir" ] && [ ! -L "$data_dir" ]; then
    find "$data_dir" -mindepth 1 -print | while IFS= read -r src; do
        rel=${src#"$data_dir"/}
        dst="$config_dir/$rel"
        if [ -d "$src" ]; then
            mkdir -p "$dst"
            continue
        fi
        if [ ! -e "$dst" ]; then
            mkdir -p "$(dirname "$dst")"
            cp -p "$src" "$dst"
        fi
    done
    rm -rf "$data_dir"
fi

if [ ! -e "$data_dir" ]; then
    ln -s "$config_dir" "$data_dir"
fi

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
