#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Theia - no-op install
# The Theia desktop client copies its own backend at attach time via the
# devcontainer protocol; no in-image install is required.
set -e
echo "theia: no build-time install needed"
