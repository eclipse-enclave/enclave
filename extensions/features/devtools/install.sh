#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install the Go linters `make lint` needs into the user tool prefix.
#
# golangci-lint and gosec come from their upstream release archives, verified
# against the release checksums, so the install never compiles them and never
# needs a newer Go than the distro one. govulncheck has no binary release and is
# built with the distro Go. GOTOOLCHAIN=local makes any future version drift a
# visible error instead of a silent 80 MB toolchain download.
set -euo pipefail

if ! declare -F enclave_curl >/dev/null 2>&1; then
    # The build runners export the network helpers; standalone runs (the CI
    # lint job, local testing) load them from the build-scripts tree.
    # shellcheck source=runtime-assets/build-scripts/lib/common.sh
    . "${ENCLAVE_BUILD_SCRIPTS_DIR:-/opt/enclave/build-scripts}/lib/common.sh"
fi

if ! command -v go >/dev/null 2>&1; then
    echo "go is required to install govulncheck" >&2
    exit 1
fi

export GOBIN="${GOBIN:-$HOME/.local/bin}"
export GOTOOLCHAIN=local
mkdir -p "$GOBIN"

GOLANGCI_LINT_VERSION="${GOLANGCI_LINT_VERSION:-v2.10.1}"
GOSEC_VERSION="${GOSEC_VERSION:-v2.23.0}"
GOVULNCHECK_VERSION="${GOVULNCHECK_VERSION:-v1.1.4}"

case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *)
        echo "unsupported architecture for the Go linters: $(uname -m)" >&2
        exit 1
        ;;
esac

# install_release_binary <name> <release-base-url> <archive> <checksums-file>
# Downloads the archive and the release checksum list, verifies the archive,
# and installs the binary named <name> found inside it into GOBIN.
install_release_binary() {
    local name="$1"
    local base_url="$2"
    local archive="$3"
    local checksums="$4"
    local workdir=""
    local binary=""
    workdir="$(mktemp -d)"

    echo "Installing ${name} from ${base_url}/${archive}"
    enclave_curl -L -o "$workdir/$archive" "$base_url/$archive"
    enclave_curl -L -o "$workdir/checksums.txt" "$base_url/$checksums"
    if ! grep -E "[[:space:]]\*?${archive}\$" "$workdir/checksums.txt" > "$workdir/expected.txt"; then
        echo "${name}: ${checksums} has no entry for ${archive}" >&2
        rm -rf "$workdir"
        return 1
    fi
    if ! (cd "$workdir" && sha256sum --check --strict --quiet expected.txt); then
        echo "${name}: checksum verification failed for ${archive}" >&2
        rm -rf "$workdir"
        return 1
    fi

    mkdir -p "$workdir/extract"
    tar -xzf "$workdir/$archive" -C "$workdir/extract"
    binary="$(find "$workdir/extract" -type f -name "$name" | head -n 1)"
    if [ -z "$binary" ]; then
        echo "${name}: archive ${archive} does not contain a ${name} binary" >&2
        rm -rf "$workdir"
        return 1
    fi
    install -m 0755 "$binary" "$GOBIN/$name"
    rm -rf "$workdir"
}

install_release_binary golangci-lint \
    "https://github.com/golangci/golangci-lint/releases/download/${GOLANGCI_LINT_VERSION}" \
    "golangci-lint-${GOLANGCI_LINT_VERSION#v}-linux-${arch}.tar.gz" \
    "golangci-lint-${GOLANGCI_LINT_VERSION#v}-checksums.txt"

install_release_binary gosec \
    "https://github.com/securego/gosec/releases/download/${GOSEC_VERSION}" \
    "gosec_${GOSEC_VERSION#v}_linux_${arch}.tar.gz" \
    "gosec_${GOSEC_VERSION#v}_checksums.txt"

echo "Installing govulncheck ${GOVULNCHECK_VERSION} with $(go version)"
enclave_retry "go install govulncheck" -- \
    go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}"

for tool in golangci-lint gosec govulncheck; do
    if [ ! -x "$GOBIN/$tool" ]; then
        echo "failed to install $tool into $GOBIN" >&2
        exit 1
    fi
done

echo "Dev linter toolchain installed: golangci-lint ${GOLANGCI_LINT_VERSION}, gosec ${GOSEC_VERSION}, govulncheck ${GOVULNCHECK_VERSION}"
