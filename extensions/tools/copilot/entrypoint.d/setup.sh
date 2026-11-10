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

if [ -n "${ENCLAVE_NATIVE_AUTH_OPS:-}" ]; then
	_copilot_settings="$COPILOT_HOME/settings.json"
	_copilot_settings_tmp=$(mktemp "$COPILOT_HOME/.protected-settings.XXXXXX")
	if [ -f "$_copilot_settings" ]; then
		if ! grep -v '^[[:space:]]*//' "$_copilot_settings" | jq -e -s 'if length == 1 and (.[0] | type) == "object" then .[0] + {storeTokenPlaintext:true} else error("expected one settings object") end' > "$_copilot_settings_tmp"; then
			rm -f "$_copilot_settings_tmp"
			echo "Cannot prepare protected Copilot settings: expected a JSON object with optional // headers" >&2
			exit 1
		fi
	else
		printf '%s\n' '{"storeTokenPlaintext":true}' > "$_copilot_settings_tmp"
	fi
	mv "$_copilot_settings_tmp" "$_copilot_settings"
	unset _copilot_settings _copilot_settings_tmp
fi

# In yolo mode, remember the project workspace as trusted for future sessions.
if [ "${ENCLAVE_YOLO:-}" = "1" ] && [ -n "${PROJECT_DIR:-}" ] && command -v jq >/dev/null 2>&1; then
	_config="$COPILOT_HOME/config.json"
	if [ -f "$_config" ]; then
		# Copilot rewrites config.json as JSONC (leading // comments); strip them for jq.
		_updated="$(grep -v '^[[:space:]]*//' "$_config" | jq --arg dir "$PROJECT_DIR" \
			'.trustedFolders = (((.trustedFolders // []) + [$dir]) | unique)' \
			2>/dev/null)" && printf '%s\n' "$_updated" > "$_config"
	else
		jq -n --arg dir "$PROJECT_DIR" '{trustedFolders:[$dir]}' > "$_config"
	fi
	unset _config _updated
fi
