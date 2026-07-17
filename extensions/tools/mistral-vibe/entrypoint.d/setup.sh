# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# shellcheck shell=bash
# Mistral Vibe extension setup
mkdir -p "$HOME/.vibe"

# Ensure uv-installed tools are on PATH
export PATH="$HOME/.local/bin:$PATH"

# In yolo mode, pre-trust the current workspace so Vibe does not force
# the folder-trust dialog before allowing auto-approved execution.
if [ "${ENCLAVE_YOLO:-}" = "1" ]; then
    if [ -n "${PROJECT_DIR:-}" ]; then
        _trusted_folders="$HOME/.vibe/trusted_folders.toml"
        if [ ! -f "$_trusted_folders" ]; then
            printf '[trusted_folders]\n"%s" = true\n' "$PROJECT_DIR" > "$_trusted_folders"
        elif ! grep -qF "$PROJECT_DIR" "$_trusted_folders" 2>/dev/null; then
            printf '"%s" = true\n' "$PROJECT_DIR" >> "$_trusted_folders"
        fi
        chmod 600 "$_trusted_folders" 2>/dev/null || true
        unset _trusted_folders
    fi
fi
