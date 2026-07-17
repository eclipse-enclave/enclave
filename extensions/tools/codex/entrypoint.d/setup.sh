# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# shellcheck shell=bash
# Codex extension setup
mkdir -p "$HOME/.codex"

# In yolo mode, default the workspace to trusted so Codex skips its trust prompt.
if [ "${ENCLAVE_YOLO:-}" = "1" ] && [ -n "${PROJECT_DIR:-}" ]; then
    _config="${ENCLAVE_TOOL_SETTINGS_TARGET:-$HOME/.codex/config.toml}"
    # Match header (`[projects."<dir>"]`) or dotted (`projects."<dir>".key`) form.
    _key="projects.\"$PROJECT_DIR\""
    # Only add when unset, so an existing entry wins and the key is never duplicated.
    if [ ! -f "$_config" ] || ! grep -qF "$_key" "$_config" 2>/dev/null; then
        printf '\n[%s]\ntrust_level = "trusted"\n' "$_key" >> "$_config"
    fi
    unset _config _key
fi
