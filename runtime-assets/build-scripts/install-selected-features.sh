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

# Used by the direct Dockerfile fallback. Generated Dockerfiles emit the same
# contiguous feature blocks as individual layers instead.
rows="$(enclave_list_enabled_features "${FEATURES-default}")"
required_names=""
while IFS=$'\t' read -r _priority name ext; do
    [ -n "$name" ] || continue
    spec="$(enclave_ext_spec "$ext")"
    required_names+="$(enclave_spec_read "$spec" '.requiresFeatures[]' '')"$'\n'
done <<< "$rows"
for ext in "$ENCLAVE_TOOLS_DIR"/*/; do
    [ -d "$ext" ] || continue
    spec="$(enclave_ext_spec "${ext%/}")"
    [ -n "$spec" ] || continue
    name="$(basename "$ext")"
    enclave_tool_is_enabled "$spec" "$name" "${AGENT_TOOLS:-}" || continue
    required_names+="$(enclave_spec_read "$spec" '.requiresFeatures[]' '')"$'\n'
done
while IFS=$'\t' read -r _priority name ext; do
    [ -n "$name" ] || continue
    ENCLAVE_FEATURES_RESOLVED=1 FEATURES="$name" "$ENCLAVE_BUILD_SCRIPTS_DIR/install-feature-apt-packages.sh"
    strict=0
    if enclave_word_list_contains "$required_names" "$name"; then strict=1; fi
    spec="$(enclave_ext_spec "$ext")"
    if [ "$(enclave_spec_read "$spec" '.needsRoot // false' false)" = true ]; then
        ENCLAVE_FEATURES_RESOLVED=1 FEATURES="$name" ENCLAVE_FEATURE_PHASE=root ENCLAVE_FEATURE_INSTALL_STRICT="$strict" \
            "$ENCLAVE_BUILD_SCRIPTS_DIR/run-feature-installs.sh"
    else
        runuser -u "${USERNAME:-agent}" -- env ENCLAVE_FEATURES_RESOLVED=1 FEATURES="$name" AGENT_TOOLS= \
            ENCLAVE_FEATURE_PHASE=user ENCLAVE_FEATURE_INSTALL_STRICT="$strict" \
            "$ENCLAVE_BUILD_SCRIPTS_DIR/run-feature-installs.sh"
    fi
    ENCLAVE_FEATURES_RESOLVED=1 FEATURES="$name" ENCLAVE_FEATURE_INSTALL_STRICT="$strict" ENCLAVE_AGENT_USER="${USERNAME:-agent}" \
        "$ENCLAVE_BUILD_SCRIPTS_DIR/install-extension-commands.sh"
done <<< "$rows"
