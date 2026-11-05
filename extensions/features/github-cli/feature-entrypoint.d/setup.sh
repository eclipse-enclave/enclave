#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

if [ "${ENCLAVE_PROTECTED_GITHUB:-}" = "1" ]; then
    protected_gh=/opt/enclave/gh-2.87.3/bin/gh
    if [ ! -x "$protected_gh" ] || ! protected_gh_version="$("$protected_gh" --version)"; then
        echo "Protected GitHub CLI installation is missing or unusable" >&2
        exit 1
    fi
    case "$protected_gh_version" in
        "gh version 2.87.3 "*) ;;
        *) echo "Protected GitHub CLI version mismatch" >&2; exit 1 ;;
    esac
    export PATH="/opt/enclave/gh-2.87.3/bin:$PATH"
    export GH_CONFIG_DIR="$HOME/.config/gh"
    export GH_HOST=github.com
fi
