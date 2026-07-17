#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install Python development tools via uv
set -e

# Install uv if not present
if ! command -v uv >/dev/null 2>&1; then
    echo "Installing uv..."
    curl -LsSf https://astral.sh/uv/install.sh | sh
    export PATH="$HOME/.local/bin:$PATH"
fi

if ! command -v uv >/dev/null 2>&1; then
    echo "uv installation failed: uv not found in PATH" >&2
    exit 1
fi

uv tool install black
uv tool install ruff
uv tool install mypy
uv tool install pytest
uv tool install ipython
uv tool install poetry
uv tool install pipenv

echo "Python development tools installed"
