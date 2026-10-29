#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -euo pipefail

tag=${1:?usage: verify-github-release.sh TAG DIRECTORY [--allow-missing]}
source=${2:?usage: verify-github-release.sh TAG DIRECTORY [--allow-missing]}
mode=${3:-}
case "$mode" in
  ''|--allow-missing) ;;
  *) echo "unknown verification mode: $mode" >&2; exit 2 ;;
esac

# The by-tag REST endpoint only finds published releases. Let gh resolve the
# draft too, then use its numeric ID for the complete REST asset metadata.
release_id=$(gh release view "$tag" --json databaseId --jq .databaseId)
assets=$(gh api "repos/$GH_REPO/releases/$release_id" \
  --jq '.assets[] | [.name, (.digest // "unknown"), (.id | tostring), .state] | @tsv')
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

for file in "$source"/*; do
  name=${file##*/}
  row=$(printf '%s\n' "$assets" | awk -F '\t' -v name="$name" '$1 == name { print; exit }')
  if [ -z "$row" ]; then
    if [ "$mode" = --allow-missing ]; then continue; fi
    echo "::error::release asset $name is missing" >&2
    exit 1
  fi
  IFS=$'\t' read -r _ digest id state <<< "$row"
  if [ "$state" != uploaded ]; then
    # GitHub documents starter as an empty entry left by a failed upload.
    # Only the publishing workflow may delete it, after this preflight passes.
    if [ "$mode" = --allow-missing ] && [ "$state" = starter ]; then continue; fi
    echo "::error::release asset $name is not fully uploaded (state: $state)" >&2
    exit 1
  fi
  local_digest="sha256:$(sha256sum "$file" | cut -d' ' -f1)"
  if [ "$digest" = unknown ]; then
    gh api "repos/$GH_REPO/releases/assets/$id" \
      -H 'Accept: application/octet-stream' > "$scratch/asset"
    digest="sha256:$(sha256sum "$scratch/asset" | cut -d' ' -f1)"
  fi
  if [ "$digest" != "$local_digest" ]; then
    echo "::error::release asset $name differs from the workflow file; do not replace a completed asset, use a new release version" >&2
    exit 1
  fi
done
