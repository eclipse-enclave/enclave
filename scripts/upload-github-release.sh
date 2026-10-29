#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Attaches the files in DIRECTORY to the draft release TAG, adding only missing
# assets and failing if a completed one differs.

set -euo pipefail

tag=${1:?usage: upload-github-release.sh TAG DIRECTORY}
source=${2:?usage: upload-github-release.sh TAG DIRECTORY}
if [ "$(gh release view "$tag" --json isDraft --jq .isDraft)" != true ]; then
  echo "::error::release $tag is published; only a draft takes assets" >&2
  exit 1
fi
# Check every overlap before filling gaps; even a same-commit rebuild can differ.
./scripts/verify-github-release.sh "$tag" "$source" --allow-missing
release_id=$(gh release view "$tag" --json databaseId --jq .databaseId)
assets=$(gh api "repos/$GH_REPO/releases/$release_id" \
  --jq '.assets[] | [.name, (.digest // "unknown"), (.id | tostring), .state] | @tsv')
while IFS=$'\t' read -r name _ id state; do
  [ -n "$name" ] || continue
  [ -f "$source/$name" ] || continue
  if [ "$state" = starter ]; then
    # Only empty entries from failed uploads may be deleted, never completed assets.
    gh api --method DELETE "repos/$GH_REPO/releases/assets/$id"
  fi
done <<< "$assets"
uploaded=$(printf '%s\n' "$assets" | awk -F '\t' '$4 == "uploaded" { print $1 }')
for asset in "$source"/*; do
  name=${asset##*/}
  if ! printf '%s\n' "$uploaded" | grep -Fxq -- "$name"; then
    gh release upload "$tag" "$asset"
  fi
done
./scripts/verify-github-release.sh "$tag" "$source"
