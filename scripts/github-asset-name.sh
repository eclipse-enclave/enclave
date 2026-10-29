#!/bin/sh
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT
#
# Prints the name GitHub gives a release asset uploaded as NAME. GitHub
# renames assets on upload: every run of characters other than ASCII letters,
# digits, "-", and "_" becomes one ".", and leading or trailing dots are
# dropped. Package versions carry "~", so a 1.0.1~~git snapshot package is
# published as 1.0.1.git. Release assets are renamed like this before
# checksums.txt is written, so the GitHub release and download.eclipse.org
# carry the same names.
#
#   github-asset-name.sh NAME...

set -eu

# The bracket ranges below are ASCII regardless of the caller's locale.
LC_ALL=C
export LC_ALL

[ $# -gt 0 ] || { echo "usage: $0 NAME..." >&2; exit 2; }
for name do
    printf '%s\n' "$name" | sed -E 's/[^A-Za-z0-9_-]+/./g; s/^\.//; s/\.$//'
done
