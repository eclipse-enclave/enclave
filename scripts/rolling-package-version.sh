#!/bin/sh
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -eu

CDPATH=
export CDPATH

: "${GITHUB_RUN_NUMBER:?GITHUB_RUN_NUMBER must be set}"

repo_root=$(cd -P "$(dirname "$0")/.." && pwd)
base_version=$(awk '/^VERSION[[:space:]]*\?=/{print $3; exit}' "$repo_root/Makefile")
if [ -z "$base_version" ]; then
    echo "VERSION was not found in Makefile" >&2
    exit 1
fi

next_version=$(printf '%s\n' "$base_version" | awk -F. 'BEGIN{OFS="."}{$NF=$NF+1; print}')
commit_sha=${GITHUB_SHA:-$(git -C "$repo_root" rev-parse HEAD)}
commit_short=$(printf '%s' "$commit_sha" | cut -c1-8)
commit_date=$(TZ=UTC git -C "$repo_root" show -s --format=%cd --date=format-local:%Y%m%d "$commit_sha")
printf '%s~git.%s.%s.%s\n' "$next_version" "$commit_date" "$GITHUB_RUN_NUMBER" "$commit_short"
