#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# The required claude-core feature installs Claude and its sandbox runtime.
set -e
command -v claude >/dev/null
claude --version
