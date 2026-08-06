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

# Chromium is installed here rather than via aptPackages: all selected
# features' aptPackages go through a single shared apt-get install, and
# `chromium` is only a real deb on Debian archives (Ubuntu ships a snap
# stub), so listing it there would fail every selected feature's packages on
# an Ubuntu base instead of just this feature.
apt-get update
if ! apt-get install -y --no-install-recommends chromium; then
    echo "vnc: cannot install chromium: this feature needs a base image whose" \
        "archive ships Chromium as a deb (the default Debian base does;" \
        "Ubuntu only ships it as a snap)" >&2
    exit 1
fi
apt-get clean

install -m 755 "$dir/vnc-supervisor" /usr/local/bin/vnc-supervisor
install -m 755 "$dir/vnc-open" /usr/local/bin/vnc-open
install -m 755 "$dir/vnc-chromium" /usr/local/bin/vnc-chromium
install -D -m 644 "$dir/waiting.html" /usr/local/share/enclave-vnc/waiting.html

# Register vnc-open as the image-wide default handler for http/https URLs
# (vnc-open's header explains why the scheme-handler registration matters).
# Merge into an existing mimeapps.list rather than truncating it, so handler
# registrations from other extensions survive; for http/https, vnc-open wins.
install -D -m 644 "$dir/enclave-vnc-open.desktop" /usr/share/applications/enclave-vnc-open.desktop
mimeapps=/etc/xdg/mimeapps.list
mkdir -p /etc/xdg
if [ ! -f "$mimeapps" ]; then
    printf '[Default Applications]\n' > "$mimeapps"
elif ! grep -q '^\[Default Applications\]' "$mimeapps"; then
    printf '\n[Default Applications]\n' >> "$mimeapps"
fi
sed -i -e '/^x-scheme-handler\/http=/d' -e '/^x-scheme-handler\/https=/d' "$mimeapps"
sed -i '/^\[Default Applications\]/a\
x-scheme-handler/http=enclave-vnc-open.desktop\
x-scheme-handler/https=enclave-vnc-open.desktop' "$mimeapps"

echo "vnc: installed chromium, supervisor, vnc-open, waiting page, and URL handler"
