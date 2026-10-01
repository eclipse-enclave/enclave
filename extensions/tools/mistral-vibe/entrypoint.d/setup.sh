# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# shellcheck shell=bash
# Mistral Vibe extension setup
mkdir -p "$HOME/.vibe"

# Ensure uv-installed tools are on PATH
export PATH="$HOME/.local/bin:$PATH"
