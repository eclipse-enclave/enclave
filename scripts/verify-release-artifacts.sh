#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -euo pipefail

source=${1:?usage: verify-release-artifacts.sh DIRECTORY CHECKSUMS_SHA256}
expected=${2:?usage: verify-release-artifacts.sh DIRECTORY CHECKSUMS_SHA256}
actual=$(sha256sum "$source/checksums.txt" | cut -d' ' -f1)
if [ "$actual" != "$expected" ]; then
  echo "::error::checksums.txt differs from the original build manifest" >&2
  exit 1
fi
scratch=$(mktemp)
trap 'rm -f "$scratch"' EXIT
# Regenerate the complete manifest, rather than just checking listed files:
# missing, extra, and changed assets must all prevent staging or promotion.
(cd "$source"; for file in *; do
  [ "$file" = checksums.txt ] || sha256sum -- "$file"
done) > "$scratch"
if ! cmp -s "$scratch" "$source/checksums.txt"; then
  echo "::error::release files differ from the original build manifest" >&2
  exit 1
fi
