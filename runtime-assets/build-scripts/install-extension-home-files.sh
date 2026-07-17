#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -euo pipefail

# Bake each enabled extension's files/home tree into the agent $HOME. Runs as the
# agent user at build time so baked files are agent-owned; kit files overwrite
# any pre-existing home files (kit wins). Only enabled features and included
# tools are materialized, matching the runtime enablement selection.

# shellcheck source=runtime-assets/build-scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"

# Features: honor defaultEnabled / the FEATURES selection.
while IFS=$'\t' read -r _priority _name extdir; do
    enclave_copy_home_files "$extdir"
done < <(enclave_list_enabled_features "${FEATURES:-default}")

# Tools: honor defaultIncluded / the AGENT_TOOLS selection.
if [ -d "$ENCLAVE_TOOLS_DIR" ]; then
    for ext in "$ENCLAVE_TOOLS_DIR"/*/; do
        [ -d "$ext" ] || continue
        ext="${ext%/}"
        name="$(basename "$ext")"
        spec="$(enclave_ext_spec "$ext")"
        if enclave_tool_is_enabled "$spec" "$name" "${AGENT_TOOLS:-all}"; then
            enclave_copy_home_files "$ext"
        fi
    done
fi
