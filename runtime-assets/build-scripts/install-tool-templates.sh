#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -euo pipefail

# shellcheck source=runtime-assets/build-scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"

enclave_require_dir "$ENCLAVE_TOOLS_DIR"
mkdir -p "$ENCLAVE_TEMPLATES_DIR"

for ext in "$ENCLAVE_TOOLS_DIR"/*/; do
    [ -d "$ext" ] || continue
    tool="$(basename "$ext")"
    for tpl in "$ext"/templates/*; do
        [ -f "$tpl" ] || continue
        cp "$tpl" "$ENCLAVE_TEMPLATES_DIR/${tool}-$(basename "$tpl")"
    done
done
