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
#
# DISPLAY is process-wide for every tool in the session, which also flips the
# behavior of anything that treats a set DISPLAY as "a GUI is available"
# (pinentry, SSH_ASKPASS, GIT_ASKPASS): those prompts render on the contained
# display instead of the terminal. The feature README documents this.
export DISPLAY="${ENCLAVE_VNC_DISPLAY:-:99}"
export BROWSER=/usr/local/bin/vnc-open
