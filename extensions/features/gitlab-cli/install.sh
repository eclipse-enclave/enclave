#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install GitLab CLI (glab)
set -e

ARCH=$(dpkg --print-architecture)
# Resolve the latest release unless pinned. A failed lookup used to produce an
# empty version and a download error that blamed a malformed URL.
if [ -z "${GLAB_VERSION:-}" ]; then
    GLAB_VERSION=$(enclave_curl -L "https://gitlab.com/api/v4/projects/34675721/releases/permalink/latest" | sed -n 's/.*"tag_name":"v\?\([^"]*\)".*/\1/p')
fi
if [ -z "$GLAB_VERSION" ]; then
    echo "could not determine the latest glab release; set GLAB_VERSION to pin one" >&2
    exit 1
fi
echo "Installing glab version ${GLAB_VERSION} for ${ARCH}"
enclave_curl -L -o /tmp/glab.deb \
    "https://gitlab.com/gitlab-org/cli/-/releases/v${GLAB_VERSION}/downloads/glab_${GLAB_VERSION}_linux_${ARCH}.deb"
dpkg -i /tmp/glab.deb || enclave_apt_get install -f -y
rm /tmp/glab.deb

echo "GitLab CLI installed: $(glab --version)"
