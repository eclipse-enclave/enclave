#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -e

# bin/codex.js resolves the vendored binary from the optional platform package
# at runtime; nothing in the tree declares a lifecycle script.
enclave-install-npm-tool --ignore-scripts @openai/codex@latest codex Codex
