#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install GitHub CLI (gh)
set -e

keyring_download="$(mktemp)"
enclave_curl -L -o "$keyring_download" https://cli.github.com/packages/githubcli-archive-keyring.gpg
gpg --dearmor -o /usr/share/keyrings/githubcli-archive-keyring.gpg < "$keyring_download"
rm -f "$keyring_download"
chmod 644 /usr/share/keyrings/githubcli-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
    > /etc/apt/sources.list.d/github-cli.list
# The package lists and archives live in the build's shared apt cache mounts,
# not in this layer; cleaning them here would only throw away the cache.
enclave_apt_get update
enclave_apt_get install -y gh

echo "GitHub CLI installed: $(gh --version | head -1)"
