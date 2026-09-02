#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Install the VNC runtime scripts. The display/VNC/browser packages
# themselves come from aptPackages in spec.yaml.
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

install -m 755 "$dir/vnc-supervisor" /usr/local/bin/vnc-supervisor
install -m 755 "$dir/vnc-open" /usr/local/bin/vnc-open
install -D -m 644 "$dir/waiting.html" /usr/local/share/enclave-vnc/waiting.html

# Make vnc-open the image-wide default handler for http/https URLs. xdg-open
# resolves the scheme handler via xdg-mime *before* falling back to $BROWSER,
# and the apt-installed chromium.desktop (plain `chromium`, no --no-sandbox)
# would otherwise win, crash on the blocked user namespaces, and silently
# drop the URL.
install -D -m 644 "$dir/enclave-vnc-open.desktop" /usr/share/applications/enclave-vnc-open.desktop
mkdir -p /etc/xdg
printf '%s\n' \
    '[Default Applications]' \
    'x-scheme-handler/http=enclave-vnc-open.desktop' \
    'x-scheme-handler/https=enclave-vnc-open.desktop' \
    > /etc/xdg/mimeapps.list

echo "vnc: installed supervisor, vnc-open, waiting page, and URL handler"
