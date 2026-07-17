# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# shellcheck shell=bash
# Claude extension setup
mkdir -p "$HOME/.claude"
export DISABLE_TELEMETRY=1
export DISABLE_ERROR_REPORTING=1
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1

if [ ! -e "$HOME/.claude.json" ] && [ ! -L "$HOME/.claude.json" ]; then
    ln -s "$HOME/.claude/.claude.json" "$HOME/.claude.json"
fi

# Pre-approve the injected Anthropic API key so Claude Code skips its interactive
# "Detected a custom API key" prompt. Claude stores this approval (the last 20
# characters of the key) in ~/.claude.json, which is not persisted under
# --ephemeral, so we seed it every run whenever a key is present. Must run after
# the .claude.json symlink so the write lands on the config volume. We also drop
# the tail from the "rejected" list so a stale rejection on a persisted config
# cannot override the pre-approval.
if [ -n "${ANTHROPIC_API_KEY:-}" ] && command -v jq >/dev/null 2>&1; then
    _key_tail="${ANTHROPIC_API_KEY: -20}"
    _claude_json="$HOME/.claude.json"
    if [ -f "$_claude_json" ]; then
        _updated="$(jq --arg k "$_key_tail" \
            '.customApiKeyResponses.approved = (((.customApiKeyResponses.approved // []) + [$k]) | unique)
             | .customApiKeyResponses.rejected = ((.customApiKeyResponses.rejected // []) - [$k])' \
            "$_claude_json" 2>/dev/null)" && printf '%s\n' "$_updated" > "$_claude_json"
    else
        printf '{"customApiKeyResponses":{"approved":["%s"],"rejected":[]}}\n' "$_key_tail" > "$_claude_json"
    fi
    unset _key_tail _claude_json _updated
fi

# Manage the Playwright MCP server in Claude Code's local scope. Must run after
# the .claude.json symlink is in place so claude's own writes go through the
# config store, not a regular file in $HOME.
if command -v claude >/dev/null 2>&1; then
    if [ "${ENCLAVE_PLAYWRIGHT_MCP:-}" = "1" ]; then
        playwright_json=$(jq -n \
            --arg cmd "$ENCLAVE_AGENT_NODE_DIR/bin/npx" \
            '{type:"stdio",command:$cmd,args:["@playwright/mcp","--headless","--no-sandbox","--browser","chromium"],env:{PLAYWRIGHT_BROWSERS_PATH:"/opt/playwright-browsers"}}')
        claude mcp add-json -s local playwright "$playwright_json" 2>/dev/null || true
    else
        claude mcp remove -s local playwright 2>/dev/null || true
    fi
fi

# In yolo mode, suppress Claude Code's interactive prompts inside the
# already-sandboxed container:
#   1. skipDangerousModePermissionPrompt in tool settings
#   2. hasTrustDialogAccepted in ~/.claude.json for the project workspace
if [ "${ENCLAVE_YOLO:-}" = "1" ]; then
    if command -v jq >/dev/null 2>&1; then
        # Inject skipDangerousModePermissionPrompt into tool settings
        if [ -n "${ENCLAVE_TOOL_SETTINGS_TARGET:-}" ] && [ -f "$ENCLAVE_TOOL_SETTINGS_TARGET" ]; then
            _tmp="$(mktemp)"
            if jq '.skipDangerousModePermissionPrompt = true' "$ENCLAVE_TOOL_SETTINGS_TARGET" > "$_tmp" 2>/dev/null; then
                mv "$_tmp" "$ENCLAVE_TOOL_SETTINGS_TARGET"
            else
                rm -f "$_tmp"
            fi
        fi

        # Pre-trust the project workspace to skip the "Quick safety check" dialog
        if [ -n "${PROJECT_DIR:-}" ]; then
            _claude_json="$HOME/.claude.json"
            if [ -f "$_claude_json" ]; then
                # Write through the path (preserves symlinks to the config store)
                _updated="$(jq --arg dir "$PROJECT_DIR" \
                    '.projects[$dir] = (.projects[$dir] // {}) + {"hasTrustDialogAccepted": true}' \
                    "$_claude_json" 2>/dev/null)" && printf '%s\n' "$_updated" > "$_claude_json"
            else
                printf '{"projects":{"%s":{"hasTrustDialogAccepted":true}}}\n' "$PROJECT_DIR" > "$_claude_json"
            fi
            unset _claude_json _updated
        fi
    fi
fi
