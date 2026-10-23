#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -euo pipefail

tag=${1:?usage: release-info.sh TAG}
if ! printf '%s\n' "$tag" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\.(0|[1-9][0-9]*))?$'; then
  echo "::error::invalid release tag: $tag" >&2
  exit 1
fi
release=$(gh release view "$tag" --json body,isDraft)
# Notes edited on the web come back with CRLF line endings.
marker=$(jq -r .body <<< "$release" | tr -d '\r' | sed -nE 's/^<!-- enclave-release: ([0-9a-f]{40}) ([1-9][0-9]*) ([0-9a-f]{64}) -->$/\1 \2 \3/p')
if [ "$(printf '%s\n' "$marker" | wc -l)" -ne 1 ] || [ -z "$marker" ]; then
  echo "::error::release must record exactly one original build provenance marker" >&2
  exit 1
fi
read -r commit run checksum <<< "$marker"
current=$(git rev-parse "refs/tags/$tag^{commit}")
if [ "$current" != "$commit" ]; then
  echo "::error::a release tag must not move: $tag now points at $current, originally $commit" >&2
  exit 1
fi
build=$(gh api "repos/$GH_REPO/actions/runs/$run")
if ! jq -e --arg commit "$commit" '.path == ".github/workflows/release.yml" and .event == "push" and .head_sha == $commit and .conclusion == "success"' <<< "$build" >/dev/null; then
  echo "::error::original run is not a successful tagged release build of $commit" >&2
  exit 1
fi
prerelease=false
case "$tag" in *-*) prerelease=true ;; esac
printf 'commit=%s\nrun-id=%s\nchecksums-sha256=%s\nversion=%s\nprerelease=%s\ndraft=%s\n' \
  "$commit" "$run" "$checksum" "${tag#v}" "$prerelease" "$(jq -r .isDraft <<< "$release")"
