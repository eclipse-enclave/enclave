#!/usr/bin/env bash
# Copyright (C) 2026 Dirk Fauth and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -euo pipefail

curl -fsS https://api.github.com/repos/google-antigravity/antigravity-cli/releases/latest | jq -r '.tag_name'
