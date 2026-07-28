#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# lint-changed.sh — run linters only on files/packages changed in the working tree.
#
# Environment:
#   LINT_GO_DIRS   space-separated top-level dirs containing Go code (default: "cmd internal extensions/tools")
#   BASE_REF       if set, diff against this ref instead of HEAD (e.g. BASE_REF=main)
#   GOCACHE        forwarded to Go tools
#   GOLANGCI_LINT_CACHE  forwarded to golangci-lint
set -euo pipefail

LINT_GO_DIRS="${LINT_GO_DIRS:-cmd internal extensions/tools}"

# --- Collect changed files ---------------------------------------------------
if [ -n "${BASE_REF:-}" ]; then
    base=$(git merge-base "$BASE_REF" HEAD)
    changed=$(git diff --name-only --diff-filter=d "$base")
else
    # staged + unstaged changes
    changed=$(git diff --name-only --diff-filter=d HEAD 2>/dev/null || true)
fi

# Always include untracked files
untracked=$(git ls-files --others --exclude-standard)
if [ -n "$untracked" ]; then
    changed=$(printf '%s\n%s' "$changed" "$untracked")
fi

# Remove blank lines
changed=$(echo "$changed" | sed '/^$/d' | sort -u)

if [ -z "$changed" ]; then
    echo "lint-changed: no changed files detected — nothing to lint."
    exit 0
fi

rc=0

echo "Checking license headers..."
# shellcheck disable=SC2086
if ! ./scripts/check-license-headers.sh $changed; then
    rc=1
fi

# --- Classify files -----------------------------------------------------------
go_files=""
sh_files=""

for f in $changed; do
    case "$f" in
        *.go)
            # Root package files and Go files under LINT_GO_DIRS are linted.
            if [[ "$f" != */* ]]; then
                go_files="$go_files $f"
            else
                for dir in $LINT_GO_DIRS; do
                    case "$f" in
                        "$dir"/*) go_files="$go_files $f"; break ;;
                    esac
                done
            fi
            ;;
        *.sh | runtime-assets/build-scripts/bin/*)
            [ -f "$f" ] && sh_files="$sh_files $f"
            ;;
    esac
done

go_files=$(echo "$go_files" | xargs)
sh_files=$(echo "$sh_files" | xargs)

if [ -z "$go_files" ] && [ -z "$sh_files" ]; then
    echo "lint-changed: no changed Go or shell files detected."
    exit "$rc"
fi

# --- Extract unique Go packages -----------------------------------------------
go_pkgs=""
if [ -n "$go_files" ]; then
    go_pkgs=$(for f in $go_files; do dirname "$f"; done | sort -u | sed 's|^|./|')
fi

# --- Run linters (accumulate errors) -----------------------------------------
if [ -n "$go_pkgs" ]; then
    echo "lint-changed: Go packages: $go_pkgs"

    echo "Running go vet..."
    # shellcheck disable=SC2086
    if ! go vet $go_pkgs; then
        rc=1
    fi

    echo "Running golangci-lint..."
    for pkg in $go_pkgs; do
        target="$pkg/..."
        [ "$pkg" = "./." ] && target="."
        if ! golangci-lint run "$target"; then
            rc=1
        fi
    done

    echo "Running gosec..."
    # shellcheck disable=SC2086
    if ! gosec -quiet $go_pkgs; then
        rc=1
    fi
fi

if [ -n "$sh_files" ]; then
    echo "lint-changed: shell files: $sh_files"
    echo "Running shellcheck..."
    # shellcheck disable=SC2086
    if ! shellcheck --severity=warning $sh_files; then
        rc=1
    fi
fi

exit "$rc"
