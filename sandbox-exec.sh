#!/bin/sh
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# enclave-sandbox-exec joins the rootless sandbox namespace for exec'd
# commands. Under a rootless daemon the session entrypoint forks the agent
# into a child user namespace (see entrypoint.sh); `docker exec` lands in the
# outer namespaces, so this wrapper — started as (rootless) container root,
# the owner of the child namespace — enters it, drops to the sandbox
# identity, and runs the command under no_new_privs like the session itself.
set -eu

pidfile=/run/enclave/sandbox.pid
uid="${ENCLAVE_SANDBOX_UID:-}"
gid="${ENCLAVE_SANDBOX_GID:-}"

case "$uid" in *[!0-9]* | "" | 0) echo "enclave-sandbox-exec: invalid ENCLAVE_SANDBOX_UID" >&2; exit 126 ;; esac
case "$gid" in *[!0-9]* | "" | 0) echo "enclave-sandbox-exec: invalid ENCLAVE_SANDBOX_GID" >&2; exit 126 ;; esac
[ "$#" -ge 1 ] || { echo "usage: enclave-sandbox-exec command [args...]" >&2; exit 126; }

if [ ! -r "$pidfile" ]; then
    echo "enclave-sandbox-exec: sandbox pid file missing; is this a rootless sandbox session?" >&2
    exit 126
fi
pid="$(cat "$pidfile")"
case "$pid" in *[!0-9]* | "") echo "enclave-sandbox-exec: invalid sandbox pid" >&2; exit 126 ;; esac

sandbox_user="${ENCLAVE_AGENT_USER:-agent}"
home="$(getent passwd "$sandbox_user" | cut -d: -f6)"
[ -n "$home" ] || home="/home/$sandbox_user"

# -w re-resolves the invoking working directory inside the joined mount
# namespace (joining a mount namespace otherwise resets cwd to /).
exec nsenter -t "$pid" -U -m -w -S "$uid" -G "$gid" -- \
    env HOME="$home" USER="$sandbox_user" LOGNAME="$sandbox_user" \
    setpriv --no-new-privs -- "$@"
