#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -euo pipefail

binary=${1:?usage: smoke-standalone-binary.sh <binary>}
if [[ "$binary" != /* ]]; then
    binary="$(pwd)/$binary"
fi

sandbox=$(mktemp -d)
trap 'rm -rf "$sandbox"' EXIT
mkdir -p "$sandbox/home" "$sandbox/run" "$sandbox/cwd"
cp "$binary" "$sandbox/run/enclave"

# A complete unversioned root from an older make install must not win.
legacy="$sandbox/data/enclave"
mkdir -p "$legacy/extensions/tools" "$legacy/extensions/features" \
    "$legacy/runtime-assets/gateway-allowlists" \
    "$legacy/runtime-assets/build-scripts" "$legacy/docs"
touch "$legacy/.dockerignore" "$legacy/Dockerfile" \
    "$legacy/Dockerfile.gateway" "$legacy/entrypoint.sh" \
    "$legacy/gateway-entrypoint.sh"

pids=()
for _ in $(seq 1 8); do
    (
        cd "$sandbox/cwd"
        HOME="$sandbox/home" \
            XDG_CONFIG_HOME="$sandbox/config" \
            XDG_STATE_HOME="$sandbox/state" \
            XDG_CACHE_HOME="$sandbox/cache" \
            XDG_DATA_HOME="$sandbox/data" \
            "$sandbox/run/enclave" tools >/dev/null
    ) &
    pids+=("$!")
done
for pid in "${pids[@]}"; do
    wait "$pid"
done

assets="$sandbox/cache/enclave/assets"
mapfile -t roots < <(find "$assets" -mindepth 1 -maxdepth 1 -type d)
if (( ${#roots[@]} != 1 )); then
    printf 'expected one extracted asset cache entry, found %d\n' "${#roots[@]}" >&2
    exit 1
fi
root=${roots[0]}
test -f "$root/.dockerignore"
test -f "$root/docs/README.md"
test -f "$root/internal/gateway/mitm/proxy.go"
test -x "$root/entrypoint.sh"
test -x "$root/gateway-entrypoint.sh"
test -x "$root/runtime-assets/build-scripts/bin/enclave-install-tool"
test ! -e "$root/AGENTS.md"
test ! -e "$root/CLAUDE.md"

rm "$root/docs/README.md"
(
    cd "$sandbox/cwd"
    HOME="$sandbox/home" \
        XDG_CONFIG_HOME="$sandbox/config" \
        XDG_STATE_HOME="$sandbox/state" \
        XDG_CACHE_HOME="$sandbox/cache" \
        XDG_DATA_HOME="$sandbox/data" \
        "$sandbox/run/enclave" tools >/dev/null
)
test -f "$root/docs/README.md"
