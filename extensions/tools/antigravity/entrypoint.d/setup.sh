#!/bin/bash
# Copyright (C) 2026 Dirk Fauth and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT
# shellcheck shell=bash

# Antigravity CLI extension setup
mkdir -p "$HOME/.gemini/config" "$HOME/.gemini/antigravity-cli"

_agy_settings="${ENCLAVE_TOOL_SETTINGS_TARGET:-$HOME/.gemini/antigravity-cli/settings.json}"

# Apply a jq filter to agy's settings.json (created if missing). Writes through
# the path so a symlink into the config store is preserved. A settings file jq
# cannot parse is left untouched; agy refuses to overwrite it either.
# Usage: _agy_update_settings <filter> [jq args...]
_agy_update_settings() {
	local filter="$1" updated
	shift
	if [ -f "$_agy_settings" ]; then
		updated="$(jq "$@" "$filter" "$_agy_settings" 2>/dev/null)" || return 0
	else
		updated="$(jq -n "$@" "$filter" 2>/dev/null)" || return 0
	fi
	printf '%s\n' "$updated" > "$_agy_settings"
}

if command -v jq >/dev/null 2>&1; then
	# Keep the feedback survey off on every start, including for persisted or
	# passed-through settings.json files that do not carry the template value.
	_agy_update_settings '.showFeedbackSurvey = false'

	# agy only talks to the Gemini API directly when modelProvider is "gemini";
	# otherwise it ignores GEMINI_API_KEY and uses the Google sign-in.
	if [ -n "${GEMINI_API_KEY:-}" ]; then
		_agy_update_settings '.modelProvider = "gemini"'
	fi

	# In yolo mode, remember the project workspace as trusted so agy skips its
	# workspace trust dialog. Only extend a list; leave other shapes alone.
	if [ "${ENCLAVE_YOLO:-}" = "1" ] && [ -n "${PROJECT_DIR:-}" ]; then
		_agy_update_settings \
			'if (.trustedWorkspaces // []) | type == "array"
			 then .trustedWorkspaces = (((.trustedWorkspaces // []) + [$dir]) | unique)
			 else . end' \
			--arg dir "$PROJECT_DIR"
	fi
fi
unset -f _agy_update_settings
unset _agy_settings
