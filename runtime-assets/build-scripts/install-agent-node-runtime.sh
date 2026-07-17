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

node_root="${ENCLAVE_AGENT_NODE_DIR}"
node_bin="${node_root}/bin/node"
npm_cli="${node_root}/lib/node_modules/npm/bin/npm-cli.js"
npx_cli="${node_root}/lib/node_modules/npm/bin/npx-cli.js"

if [ ! -x "$node_bin" ]; then
    echo "private Node runtime is missing: $node_bin" >&2
    exit 1
fi
if [ ! -r "$npm_cli" ]; then
    echo "private npm cli is missing: $npm_cli" >&2
    exit 1
fi
if [ ! -r "$npx_cli" ]; then
    echo "private npx cli is missing: $npx_cli" >&2
    exit 1
fi

mkdir -p "$node_root/bin"
printf '%s\n' '#!/bin/sh' \
    "exec \"${node_root}/bin/node\" \"${node_root}/lib/node_modules/npm/bin/npm-cli.js\" \"\$@\"" \
    > "$node_root/bin/npm"
printf '%s\n' '#!/bin/sh' \
    "exec \"${node_root}/bin/node\" \"${node_root}/lib/node_modules/npm/bin/npx-cli.js\" \"\$@\"" \
    > "$node_root/bin/npx"
chmod +x "$node_root/bin/npm" "$node_root/bin/npx"
chmod -R a+rX "$node_root"

"$node_bin" "$npm_cli" --version
