#!/bin/sh
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Installed as xclip and wl-paste. Serves the newest host image in the
# read-only image inbox to agents that read clipboard images through these
# tools, if it was imported within the paste window. Every other request
# exits 1 with no output, which callers treat as "no image" (or "no text",
# so text paste stays with the terminal). See docs/image-inbox.md.

paste_window_seconds=60

name=${0##*/}
case "$name" in
    xclip) display=${DISPLAY-} ;;
    wl-paste) display=${WAYLAND_DISPLAY-} ;;
    *) exit 1 ;;
esac

# With a real display forwarded, hand over to the real tool if one is
# installed further down PATH. Enclave never sets a display itself.
if [ -n "$display" ]; then
    old_ifs=$IFS
    IFS=:
    for dir in $PATH; do
        candidate="${dir:-.}/$name"
        # dash, bash and busybox sh all support -ef.
        # shellcheck disable=SC3013
        if [ -f "$candidate" ] && [ -x "$candidate" ] && ! [ "$candidate" -ef "$0" ]; then
            IFS=$old_ifs
            exec "$candidate" "$@"
        fi
    done
    IFS=$old_ifs
fi

list=false
target=
case "$name" in
    xclip)
        out=false
        clipboard=false
        while [ $# -gt 0 ]; do
            case "$1" in
                -o | -ou | -out) out=true ;;
                -se | -sel | -selection)
                    [ $# -gt 1 ] || exit 1
                    shift
                    case "${1-}" in c*) clipboard=true ;; *) clipboard=false ;; esac
                    ;;
                -t | -ta | -target)
                    [ $# -gt 1 ] || exit 1
                    shift
                    target=${1-}
                    ;;
            esac
            shift
        done
        "$out" && "$clipboard" || exit 1
        if [ "$target" = TARGETS ]; then
            list=true
            target=
        fi
        ;;
    wl-paste)
        while [ $# -gt 0 ]; do
            case "$1" in
                -l | --list-types) list=true ;;
                -t | --type)
                    [ $# -gt 1 ] || exit 1
                    shift
                    target=${1-}
                    ;;
                --type=*) target=${1#--type=} ;;
                -p | --primary | -w | --watch) exit 1 ;;
            esac
            shift
        done
        ;;
esac

"$list" || [ -n "$target" ] || exit 1

inbox=${ENCLAVE_IMAGE_INBOX-}
[ -n "$inbox" ] && [ -d "$inbox" ] || exit 1

# Inbox names are "<timestamp>-<hex>.<ext>", so splitting ls output is safe;
# ls -t is the one portable way to sort by modification time.
set -f
newest=
# shellcheck disable=SC2045
for entry in $(ls -t -- "$inbox" 2>/dev/null); do
    case "$entry" in
        *.png | *.jpg | *.jpeg)
            newest=$entry
            break
            ;;
    esac
done
[ -n "$newest" ] || exit 1
image="$inbox/$newest"
[ -f "$image" ] || exit 1

case "$newest" in
    *.png) mime=image/png ;;
    *) mime=image/jpeg ;;
esac

modified=$(stat -c %Y -- "$image" 2>/dev/null) || exit 1
age=$(($(date +%s) - modified))
[ "$age" -lt 0 ] && age=$((-age))
[ "$age" -le "$paste_window_seconds" ] || exit 1

if "$list"; then
    printf '%s\n' "$mime"
    exit 0
fi
[ "$target" = "$mime" ] || exit 1
exec cat -- "$image"
