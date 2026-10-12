#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install GitHub Copilot CLI (copilot)
set -e

enclave-install-npm-tool @github/copilot@latest copilot "GitHub Copilot CLI"
