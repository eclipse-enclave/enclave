# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# shellcheck shell=bash
# Gemini extension setup
mkdir -p "$HOME/.gemini"
export GEMINI_TELEMETRY_ENABLED=false

# In yolo mode, pre-trust the current workspace so Gemini CLI does not force
# the folder-trust dialog before allowing auto-approved execution.
if [ "${ENCLAVE_YOLO:-}" = "1" ] && command -v jq >/dev/null 2>&1; then
    if [ -n "${ENCLAVE_TOOL_SETTINGS_TARGET:-}" ] && [ -f "$ENCLAVE_TOOL_SETTINGS_TARGET" ]; then
        _tmp="$(mktemp)"
        if jq '.security.folderTrust.enabled = true' "$ENCLAVE_TOOL_SETTINGS_TARGET" > "$_tmp" 2>/dev/null; then
            mv "$_tmp" "$ENCLAVE_TOOL_SETTINGS_TARGET"
        else
            rm -f "$_tmp"
        fi
    fi

    if [ -n "${PROJECT_DIR:-}" ]; then
        _trusted_folders="$HOME/.gemini/trustedFolders.json"
        if [ -f "$_trusted_folders" ]; then
            _updated="$(jq --arg dir "$PROJECT_DIR" '.[$dir] = "TRUST_FOLDER"' "$_trusted_folders" 2>/dev/null)" &&
                printf '%s\n' "$_updated" > "$_trusted_folders"
        else
            printf '{\n  "%s": "TRUST_FOLDER"\n}\n' "$PROJECT_DIR" > "$_trusted_folders"
        fi
        chmod 600 "$_trusted_folders" 2>/dev/null || true
        unset _trusted_folders _updated
    fi
fi
