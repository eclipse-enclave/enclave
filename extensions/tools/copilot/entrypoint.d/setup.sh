#!/bin/bash
# Copyright (C) 2026 Dirk Fauth and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT
# shellcheck shell=bash

# Copilot extension setup
export COPILOT_HOME="$HOME/.copilot"
mkdir -p "$COPILOT_HOME"

# In yolo mode, remember the project workspace as trusted for future sessions.
if [ "${ENCLAVE_YOLO:-}" = "1" ] && [ -n "${PROJECT_DIR:-}" ] && command -v jq >/dev/null 2>&1; then
	_config="$COPILOT_HOME/config.json"
	if [ -f "$_config" ]; then
		_updated="$(jq --arg dir "$PROJECT_DIR" \
			'.trustedFolders = (((.trustedFolders // []) + [$dir]) | unique)' \
			"$_config" 2>/dev/null)" && printf '%s\n' "$_updated" > "$_config"
	else
		jq -n --arg dir "$PROJECT_DIR" '{trustedFolders:[$dir]}' > "$_config"
	fi
	unset _config _updated
fi
