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
