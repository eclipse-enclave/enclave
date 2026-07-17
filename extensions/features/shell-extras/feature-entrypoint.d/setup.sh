# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# shellcheck shell=bash
# Setup direnv hooks for automatic .envrc loading
if command -v direnv >/dev/null 2>&1; then
    eval "$(direnv hook bash)"
fi
