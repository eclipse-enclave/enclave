#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install Mistral Vibe via recommended installer (installs uv + mistral-vibe)
set -e

curl -LsSf https://mistral.ai/vibe/install.sh | bash

# Ensure PATH includes uv tool bin
export PATH="$HOME/.local/bin:$PATH"

if ! command -v vibe >/dev/null 2>&1; then
    echo "Mistral Vibe install failed: vibe binary not found" >&2
    exit 1
fi

echo "Mistral Vibe installed at: $(which vibe)"
vibe --version
