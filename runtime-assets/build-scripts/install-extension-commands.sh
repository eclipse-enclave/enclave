#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -euo pipefail

# Synthesize a feature's declarative commands.install steps at build time. This
# is the mixin-only alternative to an install.sh sidecar: a feature may declare
# apt/curl install steps as commands.install entries instead of shipping a
# script. install.sh remains the escape hatch and WINS when both are present.
#
# Reads FEATURES (space-separated names; normally a single name from the
# per-feature Dockerfile block). Honors failOnInstallError and the
# ENCLAVE_FEATURE_INSTALL_STRICT env exactly like run-feature-installs.sh.

# shellcheck source=runtime-assets/build-scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"

enclave_require_command yq
enclave_require_dir "$ENCLAVE_FEATURES_DIR"

selection="${FEATURES-}"
if [ -z "$selection" ]; then
    echo "No features selected; skipping commands.install synthesis"
    exit 0
fi

strict="${ENCLAVE_FEATURE_INSTALL_STRICT:-0}"

for name in $selection; do
    feature_dir="$ENCLAVE_FEATURES_DIR/$name"
    spec="$(enclave_ext_spec "$feature_dir")"
    [ -n "$spec" ] || continue

    # install.sh wins: the declarative commands.install is skipped entirely when
    # a feature ships an executable install.sh escape hatch.
    if [ -x "$feature_dir/install.sh" ]; then
        continue
    fi

    # mikefarah/yq v4: raw scalar output is the default (do NOT pass -r).
    count="$(yq '.commands.install | length' "$spec" 2>/dev/null)"
    case "$count" in
        '' | null | 0) continue ;;
    esac

    fail_on_install_error="$(enclave_spec_read "$spec" '.failOnInstallError // false' false)"

    echo "Synthesizing commands.install for feature: ${name}"
    i=0
    while [ "$i" -lt "$count" ]; do
        # Omitted user defaults to "0" (root), matching the sbx kit format
        # and the Go phase-routing in spec_map.go's normalizeInstallUser.
        user="$(yq ".commands.install[$i].user // \"0\"" "$spec")"
        background="$(yq ".commands.install[$i].background // false" "$spec")"
        if [ "$background" = "true" ]; then
            echo "Warning: ${name} commands.install[$i] sets background; ignored at build time" >&2
        fi
        tag="$(yq ".commands.install[$i].command | tag" "$spec")"
        status=0
        case "$tag" in
            '!!str')
                cmd="$(yq ".commands.install[$i].command" "$spec")"
                enclave_run_install_command "$user" bash -c "$cmd" || status=$?
                ;;
            '!!seq')
                set --
                while IFS= read -r arg; do
                    set -- "$@" "$arg"
                done <<EOF
$(yq ".commands.install[$i].command[]" "$spec")
EOF
                enclave_run_install_command "$user" "$@" || status=$?
                ;;
            *)
                echo "Warning: ${name} commands.install[$i] has no runnable command; skipping" >&2
                i=$((i + 1))
                continue
                ;;
        esac
        if [ "$status" -ne 0 ]; then
            if [ "$strict" = "1" ] || [ "$fail_on_install_error" = "true" ]; then
                echo "commands.install failed for feature ${name} (index $i)" >&2
                exit 1
            fi
            echo "Warning: ${name} install-command failed" >&2
            break
        fi
        i=$((i + 1))
    done
done
