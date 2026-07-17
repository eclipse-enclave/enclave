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
GLAB_VERSION=$(curl -sL "https://gitlab.com/api/v4/projects/34675721/releases/permalink/latest" | sed -n 's/.*"tag_name":"v\?\([^"]*\)".*/\1/p')
echo "Installing glab version ${GLAB_VERSION} for ${ARCH}"
curl -fsSL -o /tmp/glab.deb \
    "https://gitlab.com/gitlab-org/cli/-/releases/v${GLAB_VERSION}/downloads/glab_${GLAB_VERSION}_linux_${ARCH}.deb"
dpkg -i /tmp/glab.deb || apt-get install -f -y
rm /tmp/glab.deb

echo "GitLab CLI installed: $(glab --version)"
