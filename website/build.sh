#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Assemble the published Enclave website into $OUTPUT_DIR (default <repo>/public):
#   - static marketing site at the root (relative links, path-prefix agnostic)
#   - Docusaurus docs under /docs (absolute baseUrl injected via DOCS_BASE_URL)
#
# DOCS_BASE_URL sets the docs base path, e.g.
#   /enclave/docs/                                     (production)
#   /enclave-website-previews/pr-previews/pr-42/docs/  (preview)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${OUTPUT_DIR:-$ROOT/../public}"
DOCS_BASE_URL="${DOCS_BASE_URL:-/docs/}"

rm -rf "$OUT"
mkdir -p "$OUT"
cp "$ROOT/index.html" "$OUT/"
cp -r "$ROOT/assets" "$OUT/assets"
cp -r "$ROOT/css" "$OUT/css"

pushd "$ROOT/docs" >/dev/null
npm ci
DOCS_BASE_URL="$DOCS_BASE_URL" npm run build
popd >/dev/null

cp -r "$ROOT/docs/build" "$OUT/docs"
echo "Assembled site at $OUT (DOCS_BASE_URL=$DOCS_BASE_URL)"
