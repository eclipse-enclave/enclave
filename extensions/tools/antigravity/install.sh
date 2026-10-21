#!/bin/bash
# Copyright (C) 2026 Dirk Fauth and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install Antigravity CLI via native installer
set -e

curl -fsSL https://antigravity.google/cli/install.sh | bash

if ! command -v agy >/dev/null 2>&1; then
    echo "Antigravity CLI install failed: agy binary not found" >&2
    exit 1
fi

echo "Antigravity CLI installed at: $(which agy)"
agy --version
