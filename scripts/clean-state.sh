#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# clean-state.sh — reset the local enclave state to a clean slate.
#
# Purpose: quickly test a PR/branch without side-effects from stale images,
# volumes, caches, or host config left over from previous runs. By default it
# preserves the things that are annoying to recreate — auth volumes and host
# secrets — while removing everything else.
#
# This is intentionally self-contained (plain docker + rm; --everything may use
# dpkg/sudo for Debian package removal) so it works from any branch, even one
# whose `enclave` binary is not built or behaves differently.
# For everyday, in-app cleanup prefer `enclave cleanup` (see --help there).
#
# What gets removed by default:
#   - all enclave containers (running + stopped, incl. gateways/sessions)
#   - all enclave volumes EXCEPT auth volumes (enclave-*-auth)
#   - all enclave images (enclave:*, enclave-*:*)
#   - host config/cache dirs: ~/.enclave/* (except secrets/) and ~/.cache/enclave
#   - unused Docker build cache (pruned AFTER images are removed, so it reclaims
#     enclave's now-orphaned layers while cache still referenced by other
#     projects' images is left untouched)
#
# What is preserved by default:
#   - auth volumes  (docker volumes named enclave-*-auth)   -> --purge-auth to remove
#   - host secrets  (~/.enclave/secrets)                    -> --purge-secrets to remove
#   - installed binary/assets                                 -> --everything to remove
#
# --everything removes the preserved opt-in state above, ~/.config/enclave,
# and known enclave install artifacts (binary, assets, completions, desktop
# entry, icons). It never removes this source checkout/repository.
#
# Usage:
#   scripts/clean-state.sh [flags]
#
# Flags:
#   -n, --dry-run           Show what would be removed, change nothing
#   -y, --yes               Skip the confirmation prompt
#       --purge-auth        Also remove auth volumes (you will have to re-login)
#       --purge-secrets     Also remove host secrets (~/.enclave/secrets)
#       --everything        Remove auth/secrets and installed binary/assets (not repo)
#       --keep-images       Keep enclave images (skip image removal)
#       --keep-build-cache  Do not prune the Docker build cache
#   -h, --help              Show this help
set -euo pipefail

APP="enclave"
ICON_SIZES=(16 22 24 32 48 64 128 256 512)

dry_run=false
assume_yes=false
purge_auth=false
purge_secrets=false
everything=false
keep_images=false
prune_build_cache=true

usage() {
    awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "$0"
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        -n|--dry-run)       dry_run=true ;;
        -y|--yes)           assume_yes=true ;;
        --purge-auth)       purge_auth=true ;;
        --purge-secrets)    purge_secrets=true ;;
        --everything)       everything=true ;;
        --keep-images)      keep_images=true ;;
        --keep-build-cache) prune_build_cache=false ;;
        -h|--help)          usage; exit 0 ;;
        *) echo "Unknown option: $1" >&2; echo "Try --help" >&2; exit 2 ;;
    esac
    shift
done

if $everything; then
    purge_auth=true
    purge_secrets=true
fi

if ! command -v docker >/dev/null 2>&1; then
    echo "error: docker not found on PATH" >&2
    exit 1
fi

home="${HOME:?HOME is not set}"

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root=""
repo_candidate="$(cd -- "${script_dir}/.." && pwd -P)"
if [[ -f "${repo_candidate}/go.mod" && -f "${repo_candidate}/Makefile" && -f "${repo_candidate}/scripts/clean-state.sh" ]]; then
    repo_root="$repo_candidate"
fi

# --- Path helpers -------------------------------------------------------------

path_exists() {
    [[ -e "$1" || -L "$1" ]]
}

canonical_parent_path() {
    local path="$1"
    local dir base physical_dir

    if [[ -d "$path" && ! -L "$path" ]]; then
        if physical_dir="$(cd -- "$path" 2>/dev/null && pwd -P)"; then
            printf '%s\n' "$physical_dir"
        else
            printf '%s\n' "$path"
        fi
        return
    fi

    dir="$(dirname -- "$path")"
    base="$(basename -- "$path")"
    if [[ -d "$dir" ]] && physical_dir="$(cd -- "$dir" 2>/dev/null && pwd -P)"; then
        printf '%s/%s\n' "$physical_dir" "$base"
    else
        printf '%s\n' "$path"
    fi
}

path_is_under_repo() {
    local path="$1"
    local canonical

    [[ -n "$repo_root" ]] || return 1
    canonical="$(canonical_parent_path "$path")"
    [[ "$canonical" == "$repo_root" || "$canonical" == "${repo_root}/"* ]]
}

run_privileged() {
    if [[ "$(id -u)" -eq 0 ]]; then
        "$@"
        return $?
    fi
    if ! command -v sudo >/dev/null 2>&1; then
        return 1
    fi
    if [[ -t 0 ]]; then
        sudo "$@"
    else
        sudo -n "$@"
    fi
}

# --- Discover resources ------------------------------------------------------

# Containers: any name prefixed with "<app>-".
mapfile -t containers < <(docker ps -a --filter "name=^/${APP}-" --format '{{.Names}}' | sort -u)

# Volumes: names prefixed with "<app>-"; auth volumes match "<app>-*-auth".
mapfile -t all_volumes < <(docker volume ls --filter "name=^${APP}-" --format '{{.Name}}' | sort -u)
volumes=()
auth_volumes=()
for v in "${all_volumes[@]}"; do
    [[ -z "$v" ]] && continue
    if [[ "$v" == *-auth ]]; then
        auth_volumes+=("$v")
    else
        volumes+=("$v")
    fi
done
if $purge_auth; then
    volumes+=("${auth_volumes[@]}")
    auth_volumes=()
fi

# Images: repository "<app>" or "<app>-*" (per-tool + gateway images).
images=()
if ! $keep_images; then
    mapfile -t images < <(
        docker images --format '{{.Repository}}:{{.Tag}}' \
            | grep -E "^${APP}(-[^:]+)?:" | sort -u
    )
fi

# Host directories.
legacy_home_dir="${home}/.${APP}"
cache_dir="${home}/.cache/${APP}"
xdg_config_home="${XDG_CONFIG_HOME:-${home}/.config}"
xdg_config_dir="${xdg_config_home}/${APP}"
dirs=()
if [[ -d "$legacy_home_dir" ]]; then
    if $everything; then
        dirs+=("$legacy_home_dir")
    else
        # Remove every entry under ~/.enclave except secrets/ (unless purging).
        while IFS= read -r -d '' entry; do
            if [[ "$(basename -- "$entry")" == "secrets" ]] && ! $purge_secrets; then
                continue
            fi
            dirs+=("$entry")
        done < <(find "$legacy_home_dir" -mindepth 1 -maxdepth 1 -print0)
    fi
fi
[[ -d "$cache_dir" ]] && dirs+=("$cache_dir")
if $everything && [[ -d "$xdg_config_dir" ]]; then
    dirs+=("$xdg_config_dir")
fi

# Installation artifacts. These are only considered for --everything, and the
# source checkout containing this script is explicitly protected.
install_paths=()
skipped_repo_install_paths=()
debian_package=false

add_install_path() {
    local path="$1"
    local existing

    [[ -n "$path" ]] || return 0
    path_exists "$path" || return 0

    if path_is_under_repo "$path"; then
        for existing in "${skipped_repo_install_paths[@]}"; do
            [[ "$existing" == "$path" ]] && return
        done
        skipped_repo_install_paths+=("$path")
        return
    fi

    for existing in "${install_paths[@]}"; do
        [[ "$existing" == "$path" ]] && return
    done
    install_paths+=("$path")
}

add_data_install_paths() {
    local data_root="$1"
    local size

    [[ -n "$data_root" ]] || return 0
    add_install_path "${data_root}/${APP}"
    add_install_path "${data_root}/bash-completion/completions/${APP}"
    add_install_path "${data_root}/applications/${APP}.desktop"
    for size in "${ICON_SIZES[@]}"; do
        add_install_path "${data_root}/icons/hicolor/${size}x${size}/apps/${APP}.png"
    done
    add_install_path "${data_root}/icons/hicolor/scalable/apps/${APP}.svg"
}

is_debian_package_installed() {
    local status

    command -v dpkg-query >/dev/null 2>&1 || return 1
    status="$(dpkg-query -W -f='${db:Status-Abbrev}' "$APP" 2>/dev/null || true)"
    [[ "$status" == ii* ]]
}

collect_install_paths() {
    local default_data_home bin_path

    if is_debian_package_installed; then
        debian_package=true
    fi

    add_install_path "${home}/.local/bin/${APP}"
    if bin_path="$(type -P "$APP" 2>/dev/null)"; then
        # Also catch custom per-user installs such as ~/bin/enclave. System
        # locations are handled explicitly below (or by dpkg for Debian installs)
        # to avoid deleting unrelated package-manager store paths.
        if [[ "$bin_path" == "${home}/"* ]]; then
            add_install_path "$bin_path"
        fi
    fi

    default_data_home="${home}/.local/share"
    add_data_install_paths "$default_data_home"
    if [[ -n "${XDG_DATA_HOME:-}" && "${XDG_DATA_HOME}" != "$default_data_home" ]]; then
        add_data_install_paths "$XDG_DATA_HOME"
    fi

    add_install_path "/usr/local/bin/${APP}"
    add_data_install_paths "/usr/local/share"
    add_install_path "/usr/local/share/doc/${APP}"

    if ! $debian_package; then
        add_install_path "/usr/bin/${APP}"
        add_data_install_paths "/usr/share"
        add_install_path "/usr/share/doc/${APP}"
    fi
}

if $everything; then
    collect_install_paths
fi

# --- Report ------------------------------------------------------------------

print_list() {
    local label="$1"; shift
    if [[ $# -eq 0 ]]; then
        printf '  %s: (none)\n' "$label"
        return
    fi
    printf '  %s:\n' "$label"
    printf '    - %s\n' "$@"
}

echo "enclave clean-state plan"
print_list "containers to remove" "${containers[@]}"
print_list "volumes to remove"    "${volumes[@]}"
if ((${#auth_volumes[@]})); then
    print_list "auth volumes KEPT (use --purge-auth or --everything to remove)" "${auth_volumes[@]}"
fi
if $keep_images; then
    echo "  images: kept (--keep-images)"
else
    print_list "images to remove" "${images[@]}"
fi
print_list "host paths to remove" "${dirs[@]}"
if ! $purge_secrets && [[ -d "${legacy_home_dir}/secrets" ]]; then
    echo "  host secrets KEPT: ${legacy_home_dir}/secrets (use --purge-secrets or --everything to remove)"
fi
if $everything; then
    if $debian_package; then
        print_list "Debian package to purge" "$APP"
    fi
    print_list "installation paths to remove" "${install_paths[@]}"
    if ((${#skipped_repo_install_paths[@]})); then
        print_list "source checkout/repo paths KEPT" "${skipped_repo_install_paths[@]}"
    elif [[ -n "$repo_root" ]]; then
        echo "  source checkout/repo KEPT: ${repo_root}"
    fi
else
    echo "  installation: kept (use --everything to remove binary/assets)"
fi
if $prune_build_cache; then
    echo "  docker build cache: will prune unused layers"
else
    echo "  docker build cache: kept (--keep-build-cache)"
fi

nothing=true
((${#containers[@]}))     && nothing=false
((${#volumes[@]}))        && nothing=false
((${#images[@]}))         && nothing=false
((${#dirs[@]}))           && nothing=false
((${#install_paths[@]}))  && nothing=false
$debian_package           && nothing=false

# Build-cache prune is worthwhile on its own (it reclaims enclave's orphaned
# BuildKit layers), so only short-circuit when there is truly nothing to do.
if $nothing && ! $prune_build_cache; then
    echo
    echo "Nothing to remove — already clean."
    exit 0
fi

if $dry_run; then
    echo
    echo "(dry run — nothing removed)"
    exit 0
fi

# --- Confirm -----------------------------------------------------------------

if ! $assume_yes; then
    echo
    read -r -p "Proceed with removal? [y/N] " reply
    case "$reply" in
        y|Y|yes|Yes) ;;
        *) echo "Aborted."; exit 1 ;;
    esac
fi

# --- Execute -----------------------------------------------------------------

if ((${#containers[@]})); then
    echo "Removing containers..."
    docker rm -f "${containers[@]}" >/dev/null 2>&1 || true
fi

if ((${#volumes[@]})); then
    echo "Removing volumes..."
    docker volume rm -f "${volumes[@]}" >/dev/null 2>&1 || true
fi

if ((${#images[@]})); then
    echo "Removing images..."
    docker rmi -f "${images[@]}" >/dev/null 2>&1 || true
fi

if ((${#dirs[@]})); then
    echo "Removing host paths..."
    rm -rf "${dirs[@]}"
fi

purge_debian_package() {
    echo "Purging Debian package..."
    if ! run_privileged dpkg --purge "$APP"; then
        echo "warning: failed to purge Debian package '$APP' (try: sudo dpkg --purge $APP)" >&2
    fi
}

remove_install_path() {
    local path="$1"

    path_exists "$path" || return 0
    if rm -rf -- "$path" 2>/dev/null; then
        return
    fi
    if run_privileged rm -rf -- "$path"; then
        return
    fi
    echo "warning: failed to remove install path: $path" >&2
}

if $everything; then
    if $debian_package; then
        purge_debian_package
    fi
    if ((${#install_paths[@]})); then
        echo "Removing installation paths..."
        for path in "${install_paths[@]}"; do
            remove_install_path "$path"
        done
    fi
fi

if $prune_build_cache; then
    echo "Pruning Docker build cache..."
    docker builder prune -f >/dev/null || true
fi

echo "Clean state complete."
