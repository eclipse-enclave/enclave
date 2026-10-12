#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install the Claude Agent SDK (library only, no bin entry)
set -e

enclave-agent-npm-install @anthropic-ai/claude-agent-sdk@latest

sdk_dir="$HOME/.local/lib/node_modules/@anthropic-ai/claude-agent-sdk"
if [ ! -f "$sdk_dir/sdk.mjs" ]; then
    echo "Claude Agent SDK install failed: $sdk_dir/sdk.mjs not found" >&2
    exit 1
fi
echo "Claude Agent SDK installed at: $sdk_dir"
