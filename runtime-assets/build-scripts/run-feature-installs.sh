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
enclave_require_dir "$ENCLAVE_FEATURES_DIR"

phase="${ENCLAVE_FEATURE_PHASE:-user}"
case "$phase" in
    root|user)
        ;;
    *)
        echo "invalid ENCLAVE_FEATURE_PHASE: $phase (expected root or user)" >&2
        exit 1
        ;;
esac

selection="${FEATURES-default}"
if [ -z "$selection" ]; then
    echo "No features selected; skipping ${phase} feature install phase"
    exit 0
fi

if [ "$phase" = "root" ]; then
    # Expose the agent node runtime during root-phase installs so that
    # feature scripts (e.g. playwright) can use npm/npx.
    # Excluded from user phase: user-phase features like node-dev manage
    # their own Node.js via nvm and must not see the agent-private runtime.
    if [ -d "$ENCLAVE_AGENT_NODE_DIR/bin" ]; then
        export PATH="$ENCLAVE_AGENT_NODE_DIR/bin:$PATH"
    fi
fi

if [ "$phase" = "user" ]; then
    export PATH="$HOME/.local/bin:$PATH"
    if enclave_load_nvm_no_use; then
        nvm use default >/dev/null 2>&1 || true
    fi
fi

mapfile -t installers < <(enclave_list_feature_installers "$selection" "$phase" | sort -n -k1,1 -k2,2)
if [ "${#installers[@]}" -eq 0 ]; then
    echo "No feature install scripts for phase: ${phase}"
    exit 0
fi

strict="${ENCLAVE_FEATURE_INSTALL_STRICT:-0}"
for row in "${installers[@]}"; do
    IFS=$'\t' read -r _priority feature_name install_script <<<"$row"
    feature_dir="$(dirname "$install_script")"
    spec="$(enclave_ext_spec "$feature_dir")"
    fail_on_install_error="$(enclave_spec_read "$spec" '.failOnInstallError // false' false)"

    echo "Installing feature (${phase}): ${feature_name}"
    if ! "$install_script"; then
        if [ "$strict" = "1" ] || [ "$fail_on_install_error" = "true" ]; then
            if [ "$strict" = "1" ]; then
                echo "Feature install failed in strict mode: ${feature_name}" >&2
            else
                echo "Feature install failed and is required: ${feature_name}" >&2
            fi
            exit 1
        fi
        echo "Warning: ${feature_name} install failed" >&2
    fi
done
