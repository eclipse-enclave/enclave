#!/usr/bin/env bash
#
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT
#
# Preview the marketing site and the built docs together, the way they are
# served in production: the marketing site at "/" and the Docusaurus docs at
# "/docs/". This is the faithful integration test, so cross-links (marketing
# -> docs, and the docs "Home" link -> marketing) resolve correctly.
#
# Usage: website/preview.sh [port]   (default port: 8000)

set -euo pipefail

PORT="${1:-8000}"
WEBSITE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOCS_DIR="$WEBSITE_DIR/docs"

if ! command -v python3 >/dev/null 2>&1; then
  echo "error: python3 is required to serve the preview" >&2
  exit 1
fi

echo "==> Building docs"
if [ ! -d "$DOCS_DIR/node_modules" ]; then
  echo "    installing docs dependencies (first run)"
  (cd "$DOCS_DIR" && npm install --no-audit --no-fund)
fi
(cd "$DOCS_DIR" && npm run build)

PREVIEW_DIR="$(mktemp -d)"
cleanup() {
  echo
  echo "==> Cleaning up $PREVIEW_DIR"
  rm -rf "$PREVIEW_DIR"
}
trap cleanup EXIT

echo "==> Assembling preview root"
cp -r "$WEBSITE_DIR/index.html" "$WEBSITE_DIR/css" "$WEBSITE_DIR/assets" "$PREVIEW_DIR"/
cp -r "$DOCS_DIR/build" "$PREVIEW_DIR/docs"

echo "==> Serving at http://localhost:$PORT/  (Ctrl-C to stop)"
echo "    marketing site : http://localhost:$PORT/"
echo "    docs           : http://localhost:$PORT/docs/"
cd "$PREVIEW_DIR"
exec python3 -m http.server "$PORT"
