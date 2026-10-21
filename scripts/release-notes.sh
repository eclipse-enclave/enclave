#!/bin/sh
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT
#
# Prints the GitHub release notes for a build.
#
#   release-notes.sh rolling <commit> <assets-url>
#   release-notes.sh stable <version> <assets-url> <commit>
#
# assets-url is the GitHub download URL of the release's assets; the same
# files are published on download.eclipse.org (see docs/DEV.md, Releases).
# The Commit line records the source of a release; release.yml refuses to
# republish a version from another commit.

set -eu

channel=${1:?channel}
build=${2:?commit or version}
assets_url=${3:?assets url}
repo_url=${assets_url%/releases/download/*}
downloads_url=https://download.eclipse.org/enclave

case "$channel" in
    rolling)
        short=$(printf '%s' "$build" | cut -c1-7)
        cat <<EOT
**Rolling release from \`main\`**

Commit: [\`$short\`]($repo_url/commit/$build)
Date: $(date -u +%Y-%m-%d)

Rolling builds are rebuilt from every push to \`main\` and replaced in place; the
same files are mirrored at [$downloads_url/rolling/]($downloads_url/rolling/).
Official releases are listed under [$downloads_url/releases/]($downloads_url/releases/).

EOT
        ;;
    stable)
        commit=${4:?commit}
        short=$(printf '%s' "$commit" | cut -c1-7)
        cat <<EOT
**Enclave $build**

Commit: [\`$short\`]($repo_url/commit/$commit)

These files are also published at
[$downloads_url/releases/$build/]($downloads_url/releases/$build/).

EOT
        ;;
    *)
        echo "unknown channel: $channel" >&2
        exit 2
        ;;
esac

cat <<EOT
| File | Platform | Notes |
|------|----------|-------|
| \`enclave_*_amd64.deb\` | Ubuntu 24.04 (x86-64) | Includes all assets |
| \`enclave-*.x86_64.rpm\` | Fedora Linux (x86-64) | Includes all assets |
| \`enclave-linux-amd64\` | Linux (x86-64) | Self-contained binary with runtime assets |
| \`enclave-linux-arm64\` | Linux (arm64) | Self-contained binary with runtime assets |
| \`enclave-darwin-arm64\` | macOS (Apple Silicon) | Self-contained binary, unsigned |
| \`enclave-darwin-amd64\` | macOS (Intel) | Self-contained binary, unsigned |
| \`enclave-windows-amd64.zip\` | Windows (x86-64) | WSL2 launcher only; also install a Linux artifact inside the distribution |
| \`enclave-windows-arm64.zip\` | Windows (arm64) | WSL2 launcher only; also install a Linux artifact inside the distribution |
| \`enclave.json\` | Windows | Scoop manifest for the archives above |

\`\`\`sh
# Ubuntu 24.04 (resolves dependencies automatically)
sudo apt install ./enclave_*_amd64.deb

# Fedora Linux (resolves dependencies automatically)
sudo dnf install ./enclave-*.x86_64.rpm

# Other Linux distributions (use enclave-linux-arm64 on arm64 hosts)
sha256sum --check --ignore-missing checksums.txt
install -d ~/.local/bin
install enclave-linux-amd64 ~/.local/bin/enclave

# macOS (replace arm64 with amd64 on Intel)
shasum -a 256 --check --ignore-missing checksums.txt
xattr -d com.apple.quarantine ./enclave-darwin-arm64 2>/dev/null || true
install -d ~/.local/bin
install enclave-darwin-arm64 ~/.local/bin/enclave
\`\`\`

\`\`\`powershell
# Windows: install the launcher, then enclave itself inside WSL2
scoop install $assets_url/enclave.json
\`\`\`

The standalone binaries update themselves from the \`$channel\` channel with \`enclave self-update\` when installed into a directory you own, such as \`~/.local/bin\` (add it to \`PATH\` if needed). The updater never uses \`sudo\`; a system-wide install is updated by installing it again.

The macOS binaries are unsigned and not notarized, so Gatekeeper quarantines browser downloads and \`install\` propagates the attribute; \`curl\` or \`gh release download\` avoids it entirely.

\`enclave.exe\` is a launcher, not a native port: it forwards every argument to the Linux enclave binary inside a WSL2 distribution, which you install separately using the Linux artifacts above. Run it from a directory inside the distribution. See [docs/windows.md]($repo_url/blob/main/docs/windows.md).
EOT
