#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install repo-required Go linters in the user tool prefix.
set -euo pipefail

if ! command -v go >/dev/null 2>&1; then
    echo "go is required to install linters" >&2
    exit 1
fi

export GOBIN="${GOBIN:-$HOME/.local/bin}"
mkdir -p "$GOBIN"

GOLANGCI_LINT_VERSION="${GOLANGCI_LINT_VERSION:-v2.10.1}"
GOSEC_VERSION="${GOSEC_VERSION:-v2.23.0}"
GOVULNCHECK_VERSION="${GOVULNCHECK_VERSION:-v1.1.4}"
GO_INSTALL_RETRIES="${GO_INSTALL_RETRIES:-3}"
GO_INSTALL_RETRY_DELAY_SECONDS="${GO_INSTALL_RETRY_DELAY_SECONDS:-2}"

case "$GO_INSTALL_RETRIES" in
    ''|*[!0-9]*)
        echo "GO_INSTALL_RETRIES must be a positive integer, got: ${GO_INSTALL_RETRIES}" >&2
        exit 1
        ;;
esac
if [ "$GO_INSTALL_RETRIES" -lt 1 ]; then
    echo "GO_INSTALL_RETRIES must be >= 1, got: ${GO_INSTALL_RETRIES}" >&2
    exit 1
fi

case "$GO_INSTALL_RETRY_DELAY_SECONDS" in
    ''|*[!0-9]*)
        echo "GO_INSTALL_RETRY_DELAY_SECONDS must be a non-negative integer, got: ${GO_INSTALL_RETRY_DELAY_SECONDS}" >&2
        exit 1
        ;;
esac

is_retryable_go_error() {
    local log_file="$1"
    grep -Eiq "(Temporary failure resolving|no such host|i/o timeout|TLS handshake timeout|connect: network is unreachable|connection reset by peer|proxyconnect tcp|dial tcp: lookup|unexpected EOF|context deadline exceeded)" "$log_file"
}

go_install_with_retry() {
    local tool_name="$1"
    local module="$2"
    local attempt=1
    local log_file=""
    local exit_code=0
    local retry_delay=0

    trap 'rm -f "$log_file"' RETURN

    while true; do
        log_file="$(mktemp)"
        if go install "$module" >"$log_file" 2>&1; then
            rm -f "$log_file"
            return 0
        fi
        exit_code="$?"
        cat "$log_file" >&2

        if [ "$attempt" -ge "$GO_INSTALL_RETRIES" ]; then
            rm -f "$log_file"
            echo "failed to install ${tool_name} after ${attempt} attempts" >&2
            return "$exit_code"
        fi

        if ! is_retryable_go_error "$log_file"; then
            rm -f "$log_file"
            echo "non-retryable go install failure for ${tool_name}" >&2
            return "$exit_code"
        fi

        rm -f "$log_file"
        retry_delay=$((GO_INSTALL_RETRY_DELAY_SECONDS * attempt))
        echo "transient network failure installing ${tool_name}; retrying (attempt $((attempt + 1))/${GO_INSTALL_RETRIES}) in ${retry_delay}s" >&2
        sleep "$retry_delay"
        attempt=$((attempt + 1))
    done
}

go_install_with_retry "golangci-lint" "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}"
go_install_with_retry "gosec" "github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION}"
go_install_with_retry "govulncheck" "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}"

for tool in golangci-lint gosec govulncheck; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "failed to install $tool into PATH" >&2
        exit 1
    fi
done

echo "Dev linter toolchain installed"
