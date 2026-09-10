#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install Claude Code via native installer
set -e

curl -fsSL https://claude.ai/install.sh | bash

if ! command -v claude >/dev/null 2>&1; then
    echo "Claude Code install failed: claude binary not found" >&2
    exit 1
fi

echo "Claude installed at: $(which claude)"
claude --version

# Install sandbox runtime for Claude Code sandbox support.
# It ships its vendored artifacts prebuilt and declares no lifecycle scripts.
enclave-agent-npm-install --ignore-scripts @anthropic-ai/sandbox-runtime

# Nothing else in the tree references sandbox-runtime, so an incomplete install
# would only surface at runtime as degraded sandbox support. Assert the package
# landed and that every bin it declares exists, which is what would break if
# upstream started placing those in a postinstall.
sandbox_runtime_dir="${npm_config_prefix:-$HOME/.local}/lib/node_modules/@anthropic-ai/sandbox-runtime"
if [ ! -f "$sandbox_runtime_dir/package.json" ]; then
    echo "sandbox-runtime install failed: $sandbox_runtime_dir/package.json not found" >&2
    exit 1
fi

missing_bins=0
while IFS= read -r bin_path; do
    [ -n "$bin_path" ] || continue
    if [ ! -e "$sandbox_runtime_dir/$bin_path" ]; then
        echo "sandbox-runtime install incomplete: declared bin $bin_path is missing" >&2
        missing_bins=1
    fi
done < <(jq -r '(.bin // empty) | if type == "string" then . else .[] end' "$sandbox_runtime_dir/package.json")

if [ "$missing_bins" -ne 0 ]; then
    echo "sandbox-runtime may require lifecycle scripts; re-check the --ignore-scripts above" >&2
    exit 1
fi

echo "sandbox-runtime installed at: $sandbox_runtime_dir"
