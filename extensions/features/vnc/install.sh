#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install Chromium and the VNC runtime scripts. The display/VNC packages
# themselves come from aptPackages in spec.yaml.
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Chromium is installed here rather than via aptPackages so the failure is
# attributable: `chromium` is only a real deb on Debian archives (Ubuntu ships a
# snap stub), and an unavailable package makes the shared apt step fail with
# apt's own message and no mention of this feature. Either way the build stops
# (failOnInstallError), but this way it stops with an actionable one.
apt-get update
if ! apt-get install -y --no-install-recommends chromium; then
    echo "vnc: cannot install chromium: this feature needs a base image whose" \
        "archive ships Chromium as a deb (the default Debian base does;" \
        "Ubuntu only ships it as a snap)" >&2
    exit 1
fi
apt-get clean
rm -rf /var/lib/apt/lists/*

install -D -m 755 "$dir/bin/vnc-supervisor" /usr/local/bin/vnc-supervisor
install -D -m 755 "$dir/bin/vnc-open" /usr/local/bin/vnc-open
install -D -m 755 "$dir/bin/vnc-chromium" /usr/local/bin/vnc-chromium
install -D -m 644 "$dir/waiting.html" /usr/local/share/enclave/vnc/waiting.html

# Register vnc-open as the image-wide default handler for http/https URLs
# (vnc-open's header explains why the scheme-handler registration matters).
install -D -m 644 "$dir/enclave-vnc-open.desktop" /usr/share/applications/enclave-vnc-open.desktop
mimeapps=/etc/xdg/mimeapps.list
mkdir -p /etc/xdg
if [ ! -f "$mimeapps" ]; then
    printf '[Default Applications]\n' > "$mimeapps"
elif ! grep -q '^\[Default Applications\]' "$mimeapps"; then
    printf '\n[Default Applications]\n' >> "$mimeapps"
fi
# Merge rather than truncate, so registrations from other extensions survive.
# Both edits are scoped to the [Default Applications] section (the range ends at
# the next section header) so http/https entries in [Added Associations] and
# friends are left alone: drop any default this section already declares, then
# insert ours right after its header.
sed -i '/^\[Default Applications\]/,/^\[/ {
    /^x-scheme-handler\/http=/d
    /^x-scheme-handler\/https=/d
}' "$mimeapps"
sed -i '/^\[Default Applications\]/a\
x-scheme-handler/http=enclave-vnc-open.desktop\
x-scheme-handler/https=enclave-vnc-open.desktop' "$mimeapps"

echo "vnc: installed chromium, supervisor, vnc-open, waiting page, and URL handler"
