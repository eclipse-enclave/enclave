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

helpers_dir="${ENCLAVE_BUILD_SCRIPTS_DIR}/bin"
target_bin_dir="${ENCLAVE_AGENT_BIN_DIR:-$HOME/.local/bin}"

enclave_require_dir "$helpers_dir"
mkdir -p "$target_bin_dir"

for helper in enclave-agent-npm-install enclave-install-npm-tool enclave-install-tool; do
    src="${helpers_dir}/${helper}"
    if [ ! -f "$src" ]; then
        echo "missing helper script: $src" >&2
        exit 1
    fi
    install -m 0755 "$src" "${target_bin_dir}/${helper}"
done
