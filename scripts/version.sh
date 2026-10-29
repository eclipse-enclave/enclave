#!/bin/sh
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT
#
# Prints the version of this checkout, derived from release tags: vX.Y.Z, or
# vX.Y.Z-alpha.N, -beta.N, -rc.N for pre-releases. Only these forms order the
# same way for the updater, dpkg, and rpm; any other tag matching v*.*.*
# fails the build.
#
#   version.sh            On a release tag: its version, e.g. 1.0.0 or
#                         1.1.0-rc.1. Otherwise git describe relative to the
#                         latest release tag, e.g. 1.0.0-3-gabc1234, or
#                         0.1.1-gabc1234 before the first release.
#   version.sh --package  The deb/rpm version: a release tag with the
#                         pre-release separator as ~ (1.1.0~rc.1), otherwise
#                         a snapshot that sorts after the latest tag and
#                         before the next release or pre-release:
#                         1.0.1~~git.20260421.57.abcdef12 after v1.0.0
#                         (~~ sorts below ~alpha), and
#                         1.1.0~rc.1.git.20260421.57.abcdef12 after
#                         v1.1.0-rc.1 (the run number is included when
#                         GITHUB_RUN_NUMBER is set).
#
# In a workflow run for a release tag (GITHUB_REF_TYPE=tag) that tag wins
# when it points at HEAD, so a commit carrying several release tags, such as
# a release candidate promoted unchanged, is built as the tag that triggered
# the run. The rolling tag does not match the release pattern and is ignored.
# Output that could reach a shell through make is restricted to version
# characters; a tag that does not fit fails the build instead.

set -eu

CDPATH=
export CDPATH

mode=${1:-}
case "$mode" in
    ""|--package) ;;
    *) echo "usage: $0 [--package]" >&2; exit 2 ;;
esac

repo_root=$(cd -P "$(dirname "$0")/.." && pwd)
git_() { git -C "$repo_root" "$@"; }
describe() { git_ describe --tags --match 'v[0-9]*.[0-9]*.[0-9]*' "$@" 2>/dev/null; }

check() {
    case "$1" in
        ''|*[!0-9A-Za-z.~-]*|-*|~*)
            echo "refusing version derived from an unsafe tag name: $1" >&2
            exit 1
            ;;
    esac
}

# release_tag fails for a tag that matches the describe pattern but not the
# release grammar, before its version reaches any output.
release_tag() {
    if ! printf '%s\n' "$1" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\.(0|[1-9][0-9]*))?$'; then
        echo "tag $1 is not a release tag (vX.Y.Z or vX.Y.Z-{alpha,beta,rc}.N)" >&2
        exit 1
    fi
}

emit() {
    check "$1"
    printf '%s\n' "$1"
}

# Before the first release tag, versions build on a baseline one patch above
# the 0.1.0 the packages carried until then: their 0.1.1~git snapshots must
# sort below the 0.1.2~~git snapshots published from here on.
baseline=0.1.1

if ! git_ rev-parse --verify HEAD >/dev/null 2>&1; then
    # Not a checkout, e.g. a source tarball.
    if [ "$mode" = --package ]; then emit "$baseline"; else emit "$baseline-unknown"; fi
    exit 0
fi

exact=""
if [ "${GITHUB_REF_TYPE:-}" = tag ]; then
    case "${GITHUB_REF_NAME:-}" in
        v[0-9]*.[0-9]*.[0-9]*)
            if [ "$(git_ rev-parse --verify --quiet "refs/tags/$GITHUB_REF_NAME^{commit}" || true)" = "$(git_ rev-parse HEAD)" ]; then
                exact=$GITHUB_REF_NAME
            fi
            ;;
    esac
fi
[ -n "$exact" ] || exact=$(describe --exact-match || true)
if [ -n "$exact" ]; then
    release_tag "$exact"
    version=${exact#v}
    if [ "$mode" = --package ]; then
        emit "$(printf '%s' "$version" | tr - '~')"
    else
        emit "$version"
    fi
    exit 0
fi

latest=$(describe --abbrev=0 || true)
[ -z "$latest" ] || release_tag "$latest"
if [ "$mode" != --package ]; then
    if [ -n "$latest" ]; then
        emit "$(describe | sed 's/^v//')"
    else
        emit "$baseline-g$(git_ rev-parse --short HEAD)"
    fi
    exit 0
fi

base=${latest#v}
[ -n "$base" ] || base=$baseline
core=${base%%-*}
if [ "$core" = "$base" ]; then
    # Snapshots after a release sort below the next patch release and below
    # its pre-releases: ~~ sorts before ~alpha in dpkg and rpm alike.
    prefix=$(printf '%s\n' "$core" | awk -F. 'BEGIN{OFS="."}{$NF=$NF+1; print}')~~git
else
    # Snapshots after a pre-release sort above it and below the release.
    prefix=$(printf '%s\n' "$base" | tr - '~').git
fi
commit_date=$(TZ=UTC git_ show -s --format=%cd --date=format-local:%Y%m%d HEAD)
commit_short=$(git_ rev-parse --short=8 HEAD)
if [ -n "${GITHUB_RUN_NUMBER:-}" ]; then
    emit "$prefix.$commit_date.$GITHUB_RUN_NUMBER.$commit_short"
else
    emit "$prefix.$commit_date.$commit_short"
fi
