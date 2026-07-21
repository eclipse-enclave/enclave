#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -euo pipefail

is_candidate() {
    local path="$1"

    case "$path" in
        .github/*.yaml | .github/*.yml)
            return 1
            ;;
        LICENSE | LICENSE.md | NOTICE | NOTICE.md)
            return 1
            ;;
        */testdata/* | *.md | *.json | *.lock | *.sum | *.golden | *.svg | *.png | *.pdf | *.txt)
            return 1
            ;;
        *.go | *.sh | *.yaml | *.yml | *.js | *.ts | *.css | *.html | *.vue | *.puml | *.toml | *.conf)
            return 0
            ;;
        go.mod | Makefile | completions/enclave | .dockerignore | */.gitignore | .gitignore | */Dockerfile | Dockerfile | Dockerfile.* | debian/rules | debian/control)
            return 0
            ;;
    esac

    if [ -f "$path" ]; then
        local first_line=""
        IFS= read -r first_line < "$path" || true
        case "$first_line" in
            '#!'*) return 0 ;;
        esac
    fi

    return 1
}

tracked_candidates() {
    local path
    while IFS= read -r -d '' path; do
        if is_candidate "$path"; then
            printf '%s\0' "$path"
        fi
    done < <(git ls-files -z)
}

requested_candidates() {
    local path
    for path in "$@"; do
        if [ ! -f "$path" ]; then
            continue
        fi
        if is_candidate "$path"; then
            printf '%s\0' "$path"
        fi
    done
}

has_valid_header() {
    local path="$1"
    local header
    header="$(head -n 40 -- "$path")"

    # Anchor the copyright line so only a real comment header counts: the line
    # must start with comment punctuation (// # /* * <!-- /' ') or whitespace
    # before "Copyright". This rejects a matching phrase embedded in code, e.g.
    # a generator's `const header = ` + "`" + `// Copyright ...` raw string.
    grep -Eq "^[[:space:]/#*<!'-]*Copyright \(C\) [0-9]{4} .+ and others\.\$" <<<"$header" &&
        grep -Fq 'This program and the accompanying materials are made available under the' <<<"$header" &&
        grep -Fq 'terms of the MIT License, which is available in the project root.' <<<"$header" &&
        grep -Fq 'SPDX-License-Identifier: MIT' <<<"$header"
}

list_only=false
if [ "${1:-}" = "--list" ]; then
    list_only=true
    shift
fi

files=()
if [ "$#" -eq 0 ]; then
    mapfile -d '' -t files < <(tracked_candidates)
else
    mapfile -d '' -t files < <(requested_candidates "$@")
fi

if [ "$list_only" = true ]; then
    printf '%s\n' "${files[@]}"
    exit 0
fi

invalid=()
for path in "${files[@]}"; do
    if ! has_valid_header "$path"; then
        invalid+=("$path")
    fi
done

if [ "${#invalid[@]}" -gt 0 ]; then
    echo "Files with missing or invalid license headers:" >&2
    printf '  %s\n' "${invalid[@]}" >&2
    exit 1
fi

echo "License headers valid (${#files[@]} files checked)."
