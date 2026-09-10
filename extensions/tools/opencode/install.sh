#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -e

# Do not add --ignore-scripts: opencode-ai's postinstall is what places the
# platform binary, and its bin stub fails with an explicit error without it.
enclave-install-npm-tool opencode-ai opencode OpenCode
opencode --version
