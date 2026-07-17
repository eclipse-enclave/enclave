# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# shellcheck shell=bash
# Shared init-file helpers for enclave entrypoints.
#
# Honors spec.yaml `commands.initFiles`: seed a config file at container start,
# with a fixed ${WORKDIR}/${HOME}/${USER} substitution whitelist (WORKDIR maps
# to $PROJECT_DIR). Variables outside the whitelist are left literal.
#
# Sourced by entrypoint.sh; kept POSIX-sh compatible. Locals are _kit_-prefixed
# to avoid clobbering the sourcing shell.

_kit_log() {
    printf '%s\n' "$*" >&2
}

# enclave_write_init_file <path> <mode> <only_if_missing>
# Reads the file content from stdin. Portable: coreutils + envsubst only (no yq).
enclave_write_init_file() {
    _kit_path="$1"
    _kit_mode="$2"
    _kit_only_if_missing="$3"

    # Resolve the whitelist in the path argument. The subshell reads the printf
    # pipe, not the function's stdin (which is reserved for the content).
    # The single-quoted list is the envsubst whitelist; it must stay unexpanded.
    # shellcheck disable=SC2016
    _kit_resolved="$(printf '%s' "$_kit_path" | WORKDIR="${PROJECT_DIR:-}" envsubst '${WORKDIR} ${HOME} ${USER}')"

    # Skip branches must drain stdin before returning. The caller streams the
    # entry content in via `printf ... | enclave_write_init_file`; returning
    # without reading closes the pipe and SIGPIPEs the upstream producer, which
    # under entrypoint.sh's `set -e -o pipefail` would abort container start.
    if [ -z "$_kit_resolved" ]; then
        _kit_log "enclave: init file path resolved to empty; skipping"
        cat >/dev/null 2>&1 || true
        return 0
    fi

    if [ "$_kit_only_if_missing" = "true" ] && [ -e "$_kit_resolved" ]; then
        cat >/dev/null 2>&1 || true
        return 0
    fi

    # A write failure (unwritable parent, read-only mount) must warn and drain
    # stdin, never abort container start under the entrypoint's `set -e`.
    if ! mkdir -p "$(dirname "$_kit_resolved")" 2>/dev/null; then
        _kit_log "enclave: cannot create dir for init file $_kit_resolved; skipping"
        cat >/dev/null 2>&1 || true
        return 0
    fi

    # Substitute the whitelist into the content streamed on stdin.
    # shellcheck disable=SC2016
    if ! WORKDIR="${PROJECT_DIR:-}" envsubst '${WORKDIR} ${HOME} ${USER}' > "$_kit_resolved" 2>/dev/null; then
        _kit_log "enclave: failed to write init file $_kit_resolved; skipping"
        return 0
    fi

    if [ -n "$_kit_mode" ]; then
        if ! chmod "$_kit_mode" "$_kit_resolved" 2>/dev/null; then
            _kit_log "enclave: failed to chmod $_kit_resolved to $_kit_mode"
        fi
    fi
}

# enclave_feature_enabled <name>
# True when <name> is listed in the enabled-features manifest baked at build
# time ($HOME/.installed-features). Fails OPEN (true) when the manifest is
# absent so images built without it keep applying every baked feature — the
# prior behavior. Lets the entrypoint gate per-feature runtime hooks to the
# features actually selected by FEATURES, rather than every dir baked in.
enclave_feature_enabled() {
    _kit_feat="$1"
    _kit_manifest="${ENCLAVE_ENABLED_FEATURES_FILE:-$HOME/.installed-features}"
    [ -f "$_kit_manifest" ] || return 0
    grep -qxF "$_kit_feat" "$_kit_manifest"
}

# enclave_apply_init_files <ext_dir>
# Reads <ext_dir>/spec.yaml (fallback spec.json) with mikefarah/yq v4 and seeds
# each commands.initFiles entry. No-op if the spec or yq is absent.
enclave_apply_init_files() {
    _kit_ext_dir="$1"

    _kit_spec="$_kit_ext_dir/spec.yaml"
    if [ ! -f "$_kit_spec" ]; then
        _kit_spec="$_kit_ext_dir/spec.json"
    fi
    [ -f "$_kit_spec" ] || return 0
    command -v yq >/dev/null 2>&1 || return 0

    # mikefarah/yq v4: raw scalar output is the default (do NOT pass -r). A yq
    # failure (malformed baked spec, version drift) must never abort container
    # start under the entrypoint's `set -e`, so every read is `|| true`-guarded.
    _kit_count="$(yq '.commands.initFiles | length' "$_kit_spec" 2>/dev/null || true)"
    case "$_kit_count" in
        '' | null | 0) return 0 ;;
    esac

    _kit_i=0
    while [ "$_kit_i" -lt "$_kit_count" ]; do
        _kit_ifpath="$(yq ".commands.initFiles[$_kit_i].path // \"\"" "$_kit_spec" 2>/dev/null || true)"
        _kit_ifmode="$(yq ".commands.initFiles[$_kit_i].mode // \"\"" "$_kit_spec" 2>/dev/null || true)"
        _kit_ifonly="$(yq ".commands.initFiles[$_kit_i].onlyIfMissing // false" "$_kit_spec" 2>/dev/null || true)"
        # yq terminates scalar output with its own newline, on top of the one a
        # `content: |` block scalar already carries — streaming it straight to the
        # writer leaves a spurious trailing blank line. Capturing drops all
        # trailing newlines; re-add exactly one so the file ends with a single
        # newline.
        _kit_ifcontent="$(yq ".commands.initFiles[$_kit_i].content // \"\"" "$_kit_spec" 2>/dev/null || true)"
        printf '%s\n' "$_kit_ifcontent" |
            enclave_write_init_file "$_kit_ifpath" "$_kit_ifmode" "$_kit_ifonly"
        _kit_i=$((_kit_i + 1))
    done
}

# enclave_copy_workspace_files <ext_dir>
# Copies <ext_dir>/files/workspace/** into $PROJECT_DIR at container start,
# NEVER clobbering an existing host file (warn + skip). Verbatim copy, no
# envsubst — bundled workspace files are static. No-op when the dir is absent or
# $PROJECT_DIR is empty. Portable: coreutils + find only (no yq).
#
# $PROJECT_DIR is the user's real repo (bind-mounted). It may contain
# untrusted, agent- or clone-provided symlinks, so the copy is defensive:
#   - a destination that is (or resolves through) a symlink is skipped, so kit
#     content can never be written THROUGH a symlink to a target outside the
#     workspace, and a dangling symlink is not silently followed;
#   - every resolved destination must stay under $PROJECT_DIR;
#   - a per-file failure warns and continues instead of aborting container
#     start under the entrypoint's `set -e -o pipefail`.
enclave_copy_workspace_files() {
    _kit_ext_dir="$1"
    _kit_ws_src="$_kit_ext_dir/files/workspace"

    [ -d "$_kit_ws_src" ] || return 0
    [ -n "${PROJECT_DIR:-}" ] || return 0

    # Resolve the workspace root once for containment checks. If it cannot be
    # resolved, skip the whole copy rather than risk an unbounded destination.
    _kit_proj_real="$(cd "$PROJECT_DIR" 2>/dev/null && pwd -P)" || _kit_proj_real=""
    if [ -z "$_kit_proj_real" ]; then
        _kit_log "enclave: cannot resolve PROJECT_DIR; skipping workspace files"
        return 0
    fi

    find "$_kit_ws_src" -type f | while IFS= read -r _kit_src; do
        _kit_rel="${_kit_src#"$_kit_ws_src"/}"
        _kit_dest="$PROJECT_DIR/$_kit_rel"

        if ! enclave_copy_one_workspace_file "$_kit_src" "$_kit_dest" "$_kit_proj_real"; then
            _kit_log "enclave: skipping workspace file $_kit_dest"
        fi
    done
    return 0
}

# enclave_copy_one_workspace_file <src> <dest> <project_real>
# Copies a single workspace file, enforcing the never-clobber, no-symlink, and
# containment guarantees. Returns non-zero (caller logs + continues) on any
# skip or failure. Never fatal.
enclave_copy_one_workspace_file() {
    _kit_src="$1"
    _kit_dest="$2"
    _kit_proj_real="$3"

    # Never clobber, and never follow a symlink at the destination — including
    # a dangling one, which `-e` reports as absent. `-L` catches both.
    if [ -e "$_kit_dest" ] || [ -L "$_kit_dest" ]; then
        _kit_log "enclave: not overwriting existing workspace file $_kit_dest"
        return 1
    fi

    _kit_dest_dir="$(dirname "$_kit_dest")"
    # Reject a destination directory that does not resolve, or resolves through
    # a symlink to somewhere outside the workspace (a symlinked parent could
    # otherwise redirect the write out of PROJECT_DIR).
    _kit_dest_dir_real="$(cd "$_kit_dest_dir" 2>/dev/null && pwd -P)" || _kit_dest_dir_real=""
    if [ -n "$_kit_dest_dir_real" ]; then
        case "$_kit_dest_dir_real/" in
            "$_kit_proj_real"/*) : ;;
            *)
                _kit_log "enclave: workspace dest $_kit_dest escapes PROJECT_DIR; skipping"
                return 1
                ;;
        esac
    else
        # Parent does not exist yet: create it, but bail if mkdir fails.
        mkdir -p "$_kit_dest_dir" 2>/dev/null || return 1
        _kit_dest_dir_real="$(cd "$_kit_dest_dir" 2>/dev/null && pwd -P)" || return 1
        case "$_kit_dest_dir_real/" in
            "$_kit_proj_real"/*) : ;;
            *) return 1 ;;
        esac
    fi

    # cp -n: never overwrite (belt-and-suspenders against a TOCTOU race between
    # the check above and the copy). Failure is non-fatal.
    cp -n --preserve=mode "$_kit_src" "$_kit_dest" 2>/dev/null || return 1
    return 0
}

# enclave_run_startup_command <background> <cmd> [args...]
# Runs argv as the current (non-root) sandbox user. background="true" detaches
# it (output discarded) so a long-lived daemon does not block container start.
# A foreground failure is logged, never fatal: this is sourced under `set -e`
# in entrypoint.sh, so a failing startup command must not abort startup.
enclave_run_startup_command() {
    _kit_bg="$1"
    shift
    [ "$#" -ge 1 ] || return 0
    if [ "$_kit_bg" = "true" ]; then
        "$@" >/dev/null 2>&1 &
        return 0
    fi
    if ! "$@"; then
        _kit_log "enclave: startup command failed: $*"
    fi
}

# enclave_apply_startup_commands <ext_dir>
# Reads <ext_dir>/spec.yaml (fallback spec.json) with mikefarah/yq v4 and runs
# each commands.startup entry. No-op if the spec or yq is absent. Root-user
# entries are rejected at load time in Go; skipped defensively here too.
enclave_apply_startup_commands() {
    _kit_ext_dir="$1"

    _kit_spec="$_kit_ext_dir/spec.yaml"
    if [ ! -f "$_kit_spec" ]; then
        _kit_spec="$_kit_ext_dir/spec.json"
    fi
    [ -f "$_kit_spec" ] || return 0
    command -v yq >/dev/null 2>&1 || return 0

    # A yq failure must never abort container start under the entrypoint's
    # `set -e`, so every read is `|| true`-guarded.
    _kit_count="$(yq '.commands.startup | length' "$_kit_spec" 2>/dev/null || true)"
    case "$_kit_count" in
        '' | null | 0) return 0 ;;
    esac

    _kit_i=0
    while [ "$_kit_i" -lt "$_kit_count" ]; do
        _kit_suser="$(yq ".commands.startup[$_kit_i].user // \"\"" "$_kit_spec" 2>/dev/null || true)"
        case "$_kit_suser" in
            0 | root)
                _kit_log "enclave: skipping root startup command (index $_kit_i)"
                _kit_i=$((_kit_i + 1))
                continue
                ;;
        esac
        _kit_sbg="$(yq ".commands.startup[$_kit_i].background // false" "$_kit_spec" 2>/dev/null || true)"
        _kit_stag="$(yq ".commands.startup[$_kit_i].command | tag" "$_kit_spec" 2>/dev/null || true)"
        case "$_kit_stag" in
            '!!str')
                _kit_scmd="$(yq ".commands.startup[$_kit_i].command" "$_kit_spec" 2>/dev/null || true)"
                enclave_run_startup_command "$_kit_sbg" bash -c "$_kit_scmd"
                ;;
            '!!seq')
                set --
                while IFS= read -r _kit_sarg; do
                    set -- "$@" "$_kit_sarg"
                done <<EOF
$(yq ".commands.startup[$_kit_i].command[]" "$_kit_spec" 2>/dev/null || true)
EOF
                enclave_run_startup_command "$_kit_sbg" "$@"
                ;;
            *)
                _kit_log "enclave: startup command $_kit_i has no runnable command; skipping"
                ;;
        esac
        _kit_i=$((_kit_i + 1))
    done
}
