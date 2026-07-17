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

enclave_require_command yq
enclave_require_command apt-get
enclave_require_dir "$ENCLAVE_FEATURES_DIR"

selection="${FEATURES-default}"
if [ -z "$selection" ]; then
    echo "No features selected; skipping feature apt package install"
    exit 0
fi

declare -A seen=()
declare -a packages=()

while IFS=$'\t' read -r _priority feature_name feature_dir; do
    spec="$(enclave_ext_spec "$feature_dir")"
    added_for_feature=0
    while IFS= read -r pkg; do
        [ -n "$pkg" ] || continue
        if [ -z "${seen["$pkg"]:-}" ]; then
            seen["$pkg"]=1
            packages+=("$pkg")
            added_for_feature=1
        fi
    done < <(enclave_spec_read "$spec" '.aptPackages // [] | .[]')

    if [ "$added_for_feature" = "1" ]; then
        echo "Feature ${feature_name}: added apt packages"
    fi
done < <(enclave_list_enabled_features "$selection")

if [ "${#packages[@]}" -eq 0 ]; then
    echo "No feature apt packages selected"
    exit 0
fi

echo "Installing feature apt packages: ${packages[*]}"
# The package index lives in a BuildKit cache mount that can be pruned or
# GC'd independently of the cached apt-get update layers in earlier stages,
# so refresh it here instead of assuming those layers' lists survived.
apt-get update
apt-get install -y --no-install-recommends "${packages[@]}"
