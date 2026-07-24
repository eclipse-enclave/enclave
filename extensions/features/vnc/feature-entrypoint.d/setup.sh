# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# shellcheck shell=bash
# Point the session at the VNC feature's virtual display so X clients and
# "open in browser" flows (xdg-open, $BROWSER consumers) land on the contained
# Chromium. The stack itself is started by commands.startup in spec.yaml.
export DISPLAY="${VNC_DISPLAY:-:99}"
export BROWSER=/usr/local/bin/vnc-open
