#!/bin/sh
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -eu

CDPATH=
export CDPATH

if [ "$#" -ne 1 ]; then
    echo "usage: $0 <extracted-package-root>" >&2
    exit 2
fi

repo_root=$(cd -P "$(dirname "$0")/.." && pwd)
package_root=$1
app_root="$package_root/usr/share/enclave"

[ -f "$app_root/.dockerignore" ]
[ -f "$app_root/Dockerfile" ]
[ -f "$app_root/Dockerfile.gateway" ]
[ ! -e "$app_root/AGENTS.md" ]
[ ! -e "$app_root/CLAUDE.md" ]
diff -qr "$repo_root/docs" "$app_root/docs"
[ -f "$package_root/usr/share/doc/enclave/LICENSE.md" ]
[ -f "$package_root/usr/share/doc/enclave/NOTICE.md" ]

# Assets must reach the package byte-for-byte. Packaging toolchains rewrite
# files they mistake for host executables -- Fedora's brp-mangle-shebangs
# rewrites /bin/sh to /usr/bin/sh, which does not exist in the Alpine gateway
# image -- so compare content rather than only checking that files exist.
for asset in .dockerignore Dockerfile Dockerfile.gateway entrypoint.sh \
    gateway-entrypoint.sh extensions/tools extensions/features runtime-assets; do
    diff -qr "$repo_root/$asset" "$app_root/$asset"
done

while IFS= read -r path; do
    case "$path" in ""|\#*) continue ;; esac
    diff -qr "$repo_root/$path" "$app_root/$path"
done < "$repo_root/internal/gateway/gateway_proxy_build_inputs.txt"

mkdir -p "$package_root/home" "$package_root/isolated"
HOME="$package_root/home" XDG_CACHE_HOME="$package_root/cache" \
    "$app_root/enclave" tools >/dev/null
[ ! -e "$package_root/cache/enclave/assets" ]

cp "$app_root/enclave" "$package_root/isolated/enclave"
if HOME="$package_root/home" XDG_CACHE_HOME="$package_root/cache" \
    "$package_root/isolated/enclave" tools \
    >"$package_root/isolated/output" 2>&1; then
    echo "package binary unexpectedly carried embedded assets" >&2
    exit 1
fi
grep -q "embedded assets are unavailable" "$package_root/isolated/output"
