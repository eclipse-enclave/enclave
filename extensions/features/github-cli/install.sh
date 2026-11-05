#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install GitHub CLI (gh)
set -e

curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | \
    gpg --dearmor -o /usr/share/keyrings/githubcli-archive-keyring.gpg
chmod 644 /usr/share/keyrings/githubcli-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
    > /etc/apt/sources.list.d/github-cli.list
apt-get update
apt-get install -y gh
apt-get clean
rm -rf /var/lib/apt/lists/*

echo "GitHub CLI installed: $(gh --version | head -1)"

# Keep the protected native-file and OAuth adapters tied to a verified artifact.
protected_version=2.87.3
protected_arch="$(dpkg --print-architecture)"
case "$protected_arch" in
    amd64) protected_digest=c6e5537631fca45f277ef405ce8751d139b491e9402cc20891a003525a8773b2 ;;
    arm64) protected_digest=5f5d89563bf26751e2173b37e594065504e85b6b781c1f1832d24bf2c2b4554f ;;
    *)
        # Ordinary sessions keep the apt client; protected startup refuses
        # without the pinned one.
        echo "Protected GitHub CLI is unavailable on $protected_arch; ordinary sessions are unaffected" >&2
        exit 0
        ;;
esac
protected_tmp="$(mktemp -d)"
trap 'rm -rf "$protected_tmp"' EXIT
protected_archive="gh_${protected_version}_linux_${protected_arch}.tar.gz"
curl -fsSL "https://github.com/cli/cli/releases/download/v${protected_version}/${protected_archive}" -o "$protected_tmp/$protected_archive"
printf '%s  %s\n' "$protected_digest" "$protected_tmp/$protected_archive" | sha256sum -c -
tar -xzf "$protected_tmp/$protected_archive" -C "$protected_tmp" "gh_${protected_version}_linux_${protected_arch}/bin/gh"
install -D -m 755 "$protected_tmp/gh_${protected_version}_linux_${protected_arch}/bin/gh" "/opt/enclave/gh-${protected_version}/bin/gh"
"/opt/enclave/gh-${protected_version}/bin/gh" --version

# Login shells reset PATH from /etc/profile; keep the pinned client first there
# too in protected sessions.
cat > /etc/profile.d/enclave-protected-gh.sh <<EOF_PROFILE
if [ "\${ENCLAVE_PROTECTED_GITHUB:-}" = "1" ]; then
    PATH="/opt/enclave/gh-${protected_version}/bin:\$PATH"
    export PATH
fi
EOF_PROFILE
chmod 644 /etc/profile.d/enclave-protected-gh.sh
