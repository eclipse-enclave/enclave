#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install Claude Code via native installer
set -e

curl -fsSL https://claude.ai/install.sh | bash

if ! command -v claude >/dev/null 2>&1; then
    echo "Claude Code install failed: claude binary not found" >&2
    exit 1
fi

echo "Claude installed at: $(which claude)"
claude --version

# Keep the capture adapter tied to an integrity-checked client, independently
# of the ordinary native installation and its updater.
protected_prefix="$HOME/.local/share/enclave/claude-2.1.63"
protected_archive="$(mktemp --suffix=.tgz)"
trap 'rm -f "$protected_archive"' EXIT
curl -fsSL https://registry.npmjs.org/@anthropic-ai/claude-code/-/claude-code-2.1.63.tgz -o "$protected_archive"
protected_digest="$(sha512sum "$protected_archive")"
protected_digest="${protected_digest%% *}"
if [ "$protected_digest" != "a63c4616d39673a496e2abff73617c85cf90149dfaf8e95ebd8b2752f055aabe01fb89898c7fbc8a7485f2c1f651ff5241df7d906e42c64907c66520dab6a4a3" ]; then
    echo "Protected Claude client integrity check failed" >&2
    exit 1
fi
agent_node="${ENCLAVE_AGENT_NODE_BIN:-/opt/enclave/node/bin/node}"
agent_npm="${ENCLAVE_AGENT_NPM_BIN:-/opt/enclave/node/bin/npm}"
npm_config_prefix="$protected_prefix" "$agent_npm" install -g --ignore-scripts --no-audit --no-fund -- "$protected_archive"
protected_package="$protected_prefix/lib/node_modules/@anthropic-ai/claude-code"
if [ "$(jq -r .version "$protected_package/package.json")" != "2.1.63" ] || [ ! -f "$protected_package/cli.js" ]; then
    echo "Protected Claude client installation is incomplete" >&2
    exit 1
fi
rm "$protected_prefix/bin/claude"
printf '#!/bin/sh\nexec "%s" "%s/cli.js" --setting-sources user "$@"\n' \
    "$agent_node" "$protected_package" > "$protected_prefix/bin/claude"
chmod 755 "$protected_prefix/bin/claude"
"$protected_prefix/bin/claude" --version
rm -f "$protected_archive"
trap - EXIT

# Install sandbox runtime for Claude Code sandbox support.
# It ships its vendored artifacts prebuilt and declares no lifecycle scripts.
enclave-agent-npm-install --ignore-scripts @anthropic-ai/sandbox-runtime

# Nothing else in the tree references sandbox-runtime, so an incomplete install
# would only surface at runtime as degraded sandbox support. enclave-agent-npm-install
# already fails on a declared bin that did not land; assert the package itself
# did, which it only warns about.
sandbox_runtime_dir="${npm_config_prefix:-$HOME/.local}/lib/node_modules/@anthropic-ai/sandbox-runtime"
if [ ! -f "$sandbox_runtime_dir/package.json" ]; then
    echo "sandbox-runtime install failed: $sandbox_runtime_dir/package.json not found" >&2
    exit 1
fi

echo "sandbox-runtime installed at: $sandbox_runtime_dir"
