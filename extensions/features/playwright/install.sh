#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install Playwright browsers and MCP server for UI testing
set -e

# Ensure npm is available
if ! command -v npm >/dev/null 2>&1; then
    echo "npm is not available; enable node-dev feature or ensure Node.js is installed" >&2
    exit 1
fi

# Install @playwright/mcp globally
npm install -g --no-audit --no-fund --progress=false @playwright/mcp

# Install Chromium browser to a shared location accessible by all users.
# Use the playwright bundled with @playwright/mcp to ensure the browser
# version matches what the MCP server expects at runtime.
export PLAYWRIGHT_BROWSERS_PATH=/opt/playwright-browsers
mkdir -p "$PLAYWRIGHT_BROWSERS_PATH"
mcp_pw="$(npm root -g)/@playwright/mcp/node_modules/playwright"
node "$mcp_pw/cli.js" install-deps chromium
node "$mcp_pw/cli.js" install chromium

# Make browsers accessible and writable by non-root users. The MCP server
# creates temporary profile directories, and playwright install needs to
# write to .links/ when updating browsers.
chmod -R a+rwX "$PLAYWRIGHT_BROWSERS_PATH"

echo "Playwright MCP server and Chromium browser installed"
