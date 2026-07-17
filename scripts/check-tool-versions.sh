#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# check-tool-versions.sh — compare pinned devtools versions against latest releases
set -euo pipefail

INSTALL_SH="$(cd "$(dirname "$0")/.." && pwd)/extensions/features/devtools/install.sh"

if [[ ! -f "$INSTALL_SH" ]]; then
    echo "Error: install.sh not found at $INSTALL_SH" >&2
    exit 1
fi

if ! command -v go >/dev/null 2>&1; then
    echo "Error: go is required" >&2
    exit 1
fi

# tool name -> env var name -> go module path
declare -A TOOL_VAR=(
    [golangci-lint]=GOLANGCI_LINT_VERSION
    [gosec]=GOSEC_VERSION
    [govulncheck]=GOVULNCHECK_VERSION
)
declare -A TOOL_MODULE=(
    [golangci-lint]=github.com/golangci/golangci-lint/v2
    [gosec]=github.com/securego/gosec/v2
    [govulncheck]=golang.org/x/vuln
)
TOOLS=(golangci-lint gosec govulncheck)

# Extract pinned version from install.sh for a given env var name.
pinned_version() {
    local var="$1"
    sed -n "s/^${var}=\"\${${var}:-\(.*\)}\"/\1/p" "$INSTALL_SH" | head -1
}

# Fetch latest module version via the Go module proxy.
latest_version() {
    local module="$1"
    go list -m -versions "${module}@latest" 2>/dev/null | awk '{print $NF}'
}

# --- Check each tool ---

rc=0
printf "%-20s %-12s %-12s %s\n" "TOOL" "PINNED" "LATEST" "STATUS"
printf "%-20s %-12s %-12s %s\n" "----" "------" "------" "------"

for tool in "${TOOLS[@]}"; do
    pinned=$(pinned_version "${TOOL_VAR[$tool]}")
    latest=$(latest_version "${TOOL_MODULE[$tool]}")

    if [[ -z "$pinned" ]]; then
        printf "%-20s %-12s %-12s %s\n" "$tool" "?" "$latest" "NOT FOUND"
        rc=1
        continue
    fi
    if [[ -z "$latest" ]]; then
        printf "%-20s %-12s %-12s %s\n" "$tool" "$pinned" "?" "FETCH FAILED"
        rc=1
        continue
    fi

    if [[ "$pinned" == "$latest" ]]; then
        printf "%-20s %-12s %-12s %s\n" "$tool" "$pinned" "$latest" "up-to-date"
    else
        printf "%-20s %-12s %-12s %s\n" "$tool" "$pinned" "$latest" "OUTDATED"
        rc=1
    fi
done

exit "$rc"
