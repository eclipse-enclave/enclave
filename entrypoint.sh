#!/bin/bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Enclave entrypoint script - minimal initialization

set -e
set -o pipefail

# Ensure proper PATH
export PATH="$HOME/.local/bin:$PATH"
# Docker USER selection does not guarantee $USER is exported.
: "${USER:=$(id -un 2>/dev/null || true)}"
export USER

enclave_common_lib="${ENCLAVE_COMMON_LIB:-/opt/enclave/build-scripts/lib/common.sh}"
if [ -r "$enclave_common_lib" ]; then
    # shellcheck disable=SC1090 # Runtime asset path can be overridden by tests.
    . "$enclave_common_lib"
else
    : "${ENCLAVE_AGENT_NODE_DIR:=/opt/enclave/node}"
fi

enclave_net_lib="${ENCLAVE_NET_LIB:-/usr/local/share/enclave/net.sh}"
if [ -r "$enclave_net_lib" ]; then
    # shellcheck disable=SC1090 # Runtime asset path can be overridden by tests.
    . "$enclave_net_lib"
fi

if { [ -n "${ENCLAVE_RUNTIME_UID:-}" ] || [ -n "${ENCLAVE_RUNTIME_GID:-}" ]; } && \
   [ "${ENCLAVE_RUNTIME_UID_REMAP_DONE:-}" != "1" ]; then
    if [ "$(id -u)" != "0" ]; then
        echo "Runtime UID remap requested but entrypoint is not running as root" >&2
        exit 1
    fi
    if [ -z "${ENCLAVE_RUNTIME_UID:-}" ] || [ -z "${ENCLAVE_RUNTIME_GID:-}" ]; then
        echo "Runtime UID remap requires both ENCLAVE_RUNTIME_UID and ENCLAVE_RUNTIME_GID" >&2
        exit 1
    fi
    case "$ENCLAVE_RUNTIME_UID" in
        *[!0-9]* | "")
            echo "Runtime UID remap requires numeric UID/GID" >&2
            exit 1
            ;;
    esac
    case "$ENCLAVE_RUNTIME_GID" in
        *[!0-9]* | "")
            echo "Runtime UID remap requires numeric UID/GID" >&2
            exit 1
            ;;
    esac

    target_user="${ENCLAVE_RUNTIME_USER:-agent}"
    target_home="/home/$target_user"
    if ! id "$target_user" >/dev/null 2>&1; then
        echo "Runtime UID remap target user does not exist: $target_user" >&2
        exit 1
    fi

    current_group=$(id -gn "$target_user")
    existing_group=$(getent group "$ENCLAVE_RUNTIME_GID" | cut -d: -f1 || true)
    if [ -n "$existing_group" ]; then
        usermod -g "$existing_group" "$target_user"
    else
        groupmod -o -g "$ENCLAVE_RUNTIME_GID" "$current_group"
    fi
    if [ "$(id -u "$target_user")" != "$ENCLAVE_RUNTIME_UID" ]; then
        usermod -o -u "$ENCLAVE_RUNTIME_UID" "$target_user"
    fi
    chown -R "$ENCLAVE_RUNTIME_UID:$ENCLAVE_RUNTIME_GID" "$target_home" 2>/dev/null || true

    export HOME="$target_home"
    export USER="$target_user"
    export ENCLAVE_RUNTIME_UID_REMAP_DONE=1
    exec sudo -E -u "$target_user" -- "$0" "$@"
fi

if [ "${ENCLAVE_DNS_GATEWAY:-}" = "1" ] && command -v enclave_ensure_local_resolver >/dev/null 2>&1; then
    enclave_ensure_local_resolver
fi

if [ -n "${ENCLAVE_GATEWAY_CA_CERT_PATH:-}" ] && [ -f "$ENCLAVE_GATEWAY_CA_CERT_PATH" ]; then
    if command -v update-ca-certificates >/dev/null 2>&1; then
        update-ca-certificates >/dev/null 2>&1 || true
    fi
    system_ca_bundle="/etc/ssl/certs/ca-certificates.crt"
    if [ -r "$system_ca_bundle" ]; then
        if enclave_ca_bundle_dir="$(mktemp -d "${TMPDIR:-/tmp}/enclave-ca.XXXXXX")"; then
            enclave_ca_bundle="$enclave_ca_bundle_dir/ca-certificates.crt"
            if cat "$system_ca_bundle" "$ENCLAVE_GATEWAY_CA_CERT_PATH" > "$enclave_ca_bundle"; then
                export REQUESTS_CA_BUNDLE="$enclave_ca_bundle"
                export SSL_CERT_FILE="$enclave_ca_bundle"
            fi
        fi
    fi
    export NODE_EXTRA_CA_CERTS="$ENCLAVE_GATEWAY_CA_CERT_PATH"
fi

start_loopback_proxies() {
    if [ -z "${ENCLAVE_LOOPBACK_PORTS:-}" ]; then
        return
    fi
    if [ "${ENCLAVE_DNS_GATEWAY:-}" = "1" ]; then
        return
    fi
    if ! command -v socat >/dev/null 2>&1; then
        echo "Warning: socat not available; loopback proxy disabled"
        return
    fi
    if ! command -v enclave_resolve_bind_ip >/dev/null 2>&1 || ! command -v enclave_start_socat_loopback_proxy >/dev/null 2>&1; then
        echo "Warning: network helper unavailable; loopback proxy disabled"
        return
    fi
    bind_ip=$(enclave_resolve_bind_ip || true)
    if [ -z "$bind_ip" ]; then
        echo "Warning: unable to determine container IP; loopback proxy disabled"
        return
    fi
    IFS=',' read -r -a loopback_ports <<< "$ENCLAVE_LOOPBACK_PORTS"
    for port in "${loopback_ports[@]}"; do
        if [ -z "$port" ]; then
            continue
        fi
        enclave_start_socat_loopback_proxy "$port" "$bind_ip"
        echo "Loopback proxy enabled on port ${port}"
    done
}

start_loopback_proxies

start_ide_bridge_proxies() {
    if [ -z "${ENCLAVE_IDE_BRIDGE_PORTS:-}" ]; then return; fi
    if [ "${ENCLAVE_DNS_GATEWAY:-}" = "1" ]; then return; fi
    if ! command -v socat >/dev/null 2>&1; then
        echo "Warning: socat not available; IDE bridge disabled"
        return
    fi
    IFS=',' read -r -a ide_ports <<< "$ENCLAVE_IDE_BRIDGE_PORTS"
    for port in "${ide_ports[@]}"; do
        [ -z "$port" ] && continue
        socat "TCP-LISTEN:${port},reuseaddr,fork,bind=127.0.0.1" \
              "TCP:host.docker.internal:${port}" >/dev/null 2>&1 &
        echo "IDE bridge proxy enabled on port ${port}"
    done
}
start_ide_bridge_proxies

agent_label="${TOOL:-${AGENT_NAME:-$USER}}"

# Link shared auth files into the tool config dir when provided.
if [ -n "$ENCLAVE_AUTH_DIR" ] && [ -n "$ENCLAVE_AUTH_FILES" ] && [ -n "$ENCLAVE_TOOL_CONFIG_DIR" ]; then
    auth_reconcile_lib="${ENCLAVE_AUTH_RECONCILE_LIB:-/usr/local/share/enclave/auth-reconcile.sh}"
    if [ ! -r "$auth_reconcile_lib" ]; then
        echo "Auth reconciliation library missing: $auth_reconcile_lib" >&2
        exit 1
    fi
    # shellcheck disable=SC1090 # Runtime asset path can be overridden by tests.
    . "$auth_reconcile_lib"

    IFS=',' read -r -a auth_files <<< "$ENCLAVE_AUTH_FILES"
    enclave_sync_shared_auth "$agent_label" "$ENCLAVE_TOOL_CONFIG_DIR" "$ENCLAVE_AUTH_DIR" "" "1" "${auth_files[@]}"
fi

# Link feature auth files into their respective config dirs when provided.
# Format: feat:config_dir:file1,file2|feat2:config_dir2:file3,file4
if [ -n "${ENCLAVE_FEATURE_AUTH_MAP:-}" ]; then
    IFS='|' read -r -a feature_entries <<< "$ENCLAVE_FEATURE_AUTH_MAP"
    for entry in "${feature_entries[@]}"; do
        if [ -z "$entry" ]; then
            continue
        fi
        IFS=':' read -r feat_name feat_config_dir feat_files <<< "$entry"
        if [ -z "$feat_name" ] || [ -z "$feat_config_dir" ] || [ -z "$feat_files" ]; then
            continue
        fi
        feat_auth_dir="$HOME/.enclave-feature-auth/$feat_name"
        feat_config_path="$HOME/$feat_config_dir"
        mkdir -p "$feat_auth_dir" "$feat_config_path"
        IFS=',' read -r -a feat_auth_files <<< "$feat_files"
        for auth_file in "${feat_auth_files[@]}"; do
            if [ -z "$auth_file" ]; then
                continue
            fi
            auth_subdir=$(dirname "$auth_file")
            if [ "$auth_subdir" != "." ]; then
                mkdir -p "$feat_auth_dir/$auth_subdir"
                mkdir -p "$feat_config_path/$auth_subdir"
            fi
            auth_path="$feat_auth_dir/$auth_file"
            config_path="$feat_config_path/$auth_file"
            # Skip if config_path is a directory (don't clobber)
            if [ -d "$config_path" ] && [ ! -L "$config_path" ]; then
                continue
            fi
            # Seed auth from config if auth is empty and a real config file exists
            if [ -e "$config_path" ] && [ ! -L "$config_path" ] && [ ! -s "$auth_path" ]; then
                cp -f "$config_path" "$auth_path" 2>/dev/null || true
                chmod 600 "$auth_path" 2>/dev/null || true
            fi
            # Remove stale symlinks when auth is missing
            if [ -L "$config_path" ] && [ ! -e "$auth_path" ]; then
                rm -f "$config_path"
                continue
            fi
            # Ensure auth file exists so the symlink target is valid
            if [ ! -e "$auth_path" ]; then
                touch "$auth_path"
                chmod 600 "$auth_path"
            fi
            # Always symlink config -> auth so writes go to the feature auth store
            if [ ! -L "$config_path" ] || [ "$(readlink "$config_path")" != "$auth_path" ]; then
                ln -sf "$auth_path" "$config_path"
            fi
        done
    done
fi

# Apply tool settings template if configured.
if [ -n "$ENCLAVE_TOOL_SETTINGS_TEMPLATE" ] && [ -n "$ENCLAVE_TOOL_SETTINGS_TARGET" ]; then
    if [ -f "$ENCLAVE_TOOL_SETTINGS_TEMPLATE" ]; then
        if [ ! -f "$ENCLAVE_TOOL_SETTINGS_TARGET" ]; then
            mkdir -p "$(dirname "$ENCLAVE_TOOL_SETTINGS_TARGET")"
            cp "$ENCLAVE_TOOL_SETTINGS_TEMPLATE" "$ENCLAVE_TOOL_SETTINGS_TARGET"
        fi
    else
        echo "Warning: settings template missing at $ENCLAVE_TOOL_SETTINGS_TEMPLATE"
    fi
fi

# Source nvm as a user-node fallback only when node is missing from PATH.
if ! command -v node >/dev/null 2>&1 && command -v enclave_load_nvm_no_use >/dev/null 2>&1 && enclave_load_nvm_no_use "$HOME/.nvm"; then
    default_version="$(nvm version default 2>/dev/null || true)"
    if [ -n "$default_version" ] && [ "$default_version" != "N/A" ] && [ "$default_version" != "none" ] && [ "$default_version" != "system" ]; then
        nvm use default >/dev/null 2>&1 || true
    fi
fi

# Create Python virtual environment if it doesn't exist in the project
if [ -n "$PROJECT_DIR" ] && [ ! -d "$PROJECT_DIR/.venv" ] && { [ -f "$PROJECT_DIR/requirements.txt" ] || [ -f "$PROJECT_DIR/pyproject.toml" ] || [ -f "$PROJECT_DIR/setup.py" ]; }; then
    if [ "${ENCLAVE_PROJECT_MOUNT:-writable}" = "readonly" ]; then
        echo "Python project detected, but project mount is read-only. Skipping venv creation."
    elif [ ! -w "$PROJECT_DIR" ]; then
        echo "Python project detected, but project directory is not writable. Skipping venv creation."
    elif command -v uv >/dev/null 2>&1; then
        echo "Python project detected, creating virtual environment..."
        if (cd "$PROJECT_DIR" && uv venv .venv); then
            echo "Virtual environment created at .venv/"
            echo "   Activate with: source .venv/bin/activate"
        else
            echo "Warning: failed to create Python virtual environment. Continuing without .venv."
        fi
    else
        echo "Python project detected, but uv is not available. Skipping venv creation."
    fi
fi

# Set proper permissions on mounted SSH directory if it exists
if [ -d "$HOME/.ssh" ]; then
    # Ensure correct permissions for SSH directory and files
    chmod 700 "$HOME/.ssh" 2>/dev/null || true
    chmod 600 "$HOME/.ssh/"* 2>/dev/null || true
    chmod 644 "$HOME/.ssh/"*.pub 2>/dev/null || true
    chmod 644 "$HOME/.ssh/authorized_keys" 2>/dev/null || true
    chmod 644 "$HOME/.ssh/known_hosts" 2>/dev/null || true
    echo "SSH directory permissions configured"
fi

# Source extension setup scripts for the current tool
enclave_tools_dir="${ENCLAVE_TOOLS_DIR:-/opt/enclave/extensions/tools}"
enclave_features_dir="${ENCLAVE_FEATURES_DIR:-/opt/enclave/extensions/features}"
if [ -d "$enclave_tools_dir/$agent_label/entrypoint.d" ]; then
    for script in "$enclave_tools_dir/$agent_label/entrypoint.d"/*.sh; do
        if [ -f "$script" ]; then
            # shellcheck disable=SC1090 # Sourcing tool-provided entrypoints at runtime.
            . "$script"
        fi
    done
fi

# Source the shipped kit-init helper up front so its feature-enablement gate is
# available to every feature loop below, including the feature-entrypoint.d
# loop.
if [ -f /usr/local/share/enclave/kit-init.sh ]; then
    # shellcheck disable=SC1091 # Sourcing shipped runtime helper at runtime.
    . /usr/local/share/enclave/kit-init.sh
fi

# Source feature entrypoint scripts for enabled features. These run regardless
# of which TOOL is used, but are gated on build-time enablement: a feature dir
# may be baked into the image without having been selected by FEATURES (the
# developer-fallback `docker build .` copies the whole tree), and a non-selected
# feature's install never ran, so its runtime entrypoint must not fire either.
# enclave_feature_enabled fails open when the manifest is absent (or the
# helper is unavailable), preserving the prior all-features behavior.
for ext_dir in "$enclave_features_dir"/*/; do
    if command -v enclave_feature_enabled >/dev/null 2>&1; then
        enclave_feature_enabled "$(basename "${ext_dir%/}")" || continue
    fi
    if [ -d "${ext_dir}feature-entrypoint.d" ]; then
        for script in "${ext_dir}feature-entrypoint.d"/*.sh; do
            # shellcheck disable=SC1090 # Sourcing feature entrypoints at runtime.
            [ -f "$script" ] && . "$script"
        done
    fi
done

# Seed extension init files declared in spec.yaml commands.initFiles, copy any
# files/workspace trees into the project (never clobbering host files), then run
# any commands.startup entries. ALL init files and workspace files land before
# ANY startup command so a startup daemon can depend on seeded config from any
# feature.
if command -v enclave_apply_init_files >/dev/null 2>&1; then
    # Feature loops are gated on build-time enablement (see the
    # feature-entrypoint.d loop above). The selected tool is always applied.
    enclave_apply_init_files "$enclave_tools_dir/$agent_label"
    for ext_dir in "$enclave_features_dir"/*/; do
        enclave_feature_enabled "$(basename "${ext_dir%/}")" || continue
        enclave_apply_init_files "${ext_dir%/}"
    done
    enclave_copy_workspace_files "$enclave_tools_dir/$agent_label"
    for ext_dir in "$enclave_features_dir"/*/; do
        enclave_feature_enabled "$(basename "${ext_dir%/}")" || continue
        enclave_copy_workspace_files "${ext_dir%/}"
    done
    enclave_apply_startup_commands "$enclave_tools_dir/$agent_label"
    for ext_dir in "$enclave_features_dir"/*/; do
        enclave_feature_enabled "$(basename "${ext_dir%/}")" || continue
        enclave_apply_startup_commands "${ext_dir%/}"
    done
fi

# Translate host direnv approvals to container paths
if [ -d "/tmp/host_direnv_allow" ] && [ -n "$PROJECT_DIR" ] && [ -f "$PROJECT_DIR/.envrc" ] && [ -n "$HOST_PROJECT_DIR" ]; then
    mkdir -p "$HOME/.local/share/direnv/allow"

    # The host .envrc path
    host_envrc_path="$HOST_PROJECT_DIR/.envrc"

    # Calculate the expected hash for the host path + current .envrc content
    # This is how direnv validates approvals
    expected_host_hash=$(printf "%s\n" "$host_envrc_path" | cat - "$PROJECT_DIR/.envrc" | sha256sum | cut -d' ' -f1)

    # If a valid approval exists for the current .envrc content, create a corresponding approval int the container
    if [ -f "/tmp/host_direnv_allow/$expected_host_hash" ]; then
        approved_path=$(cat "/tmp/host_direnv_allow/$expected_host_hash")
        if [ "$approved_path" = "$host_envrc_path" ]; then
            container_hash=$(printf "%s\n" "$PROJECT_DIR/.envrc" | cat - "$PROJECT_DIR/.envrc" | sha256sum | cut -d' ' -f1)
            echo "$PROJECT_DIR/.envrc" > "$HOME/.local/share/direnv/allow/$container_hash"
            echo "Translated direnv approval from host to container"
        fi
    fi
fi

# Set up git config for commits inside container
if [ -f "/tmp/host_gitconfig" ]; then
    cp /tmp/host_gitconfig "$HOME/.gitconfig"
else
    cat > "$HOME/.gitconfig" << EOF
[user]
    email = ${USER}@enclave
    name = ${USER^} (enclave)
[init]
    defaultBranch = main
EOF
    echo "Using default git identity (${USER}@enclave). Configure ~/.gitconfig on host to customize."
fi

# Disable commit/tag signing inside the container — signing keys from the
# host are not available, so signed commits would always fail.
git config --global commit.gpgsign false
git config --global tag.gpgsign false

normalize_devcontainer_paths() {
    local remote_user="$ENCLAVE_DEVCONTAINER_REMOTE_USER"
    if [ -z "$remote_user" ]; then
        echo "Skipping devcontainer path normalization: remoteUser is not applied"
        return
    fi
    local remote_home
    if [ "$remote_user" = "root" ]; then
        remote_home="/root"
    else
        remote_home="/home/$remote_user"
    fi
    local current_home="$HOME"
    if [ -z "$current_home" ] || [ "$current_home" = "$remote_home" ]; then
        local current_user=""
        local passwd_home=""
        current_user="$(id -un 2>/dev/null || true)"
        if [ -n "$current_user" ]; then
            passwd_home="$(getent passwd "$current_user" 2>/dev/null | cut -d: -f6 || true)"
        fi
        if [ -n "$passwd_home" ]; then
            current_home="$passwd_home"
        elif [ -n "$current_user" ] && [ -d "/home/$current_user" ]; then
            current_home="/home/$current_user"
        elif [ "$current_user" = "root" ]; then
            current_home="/root"
        fi
        if [ -n "$current_home" ]; then
            export HOME="$current_home"
        fi
    fi
    if [ -z "$current_home" ] || [ "$current_home" = "$remote_home" ]; then
        return
    fi
    local changed=0
    local -a default_home_vars=(
        NPM_CONFIG_PREFIX
        PNPM_HOME
        NVM_DIR
        BUN_INSTALL
        COREPACK_HOME
        COREPACK_ROOT
        VOLTA_HOME
        FNM_DIR
        PNPM_STORE_DIR
        YARN_CACHE_FOLDER
        XDG_CACHE_HOME
        XDG_CONFIG_HOME
        XDG_DATA_HOME
        npm_config_prefix
        npm_config_cache
        npm_config_userconfig
    )
    local -a extra_rewrite_vars=()
    local extra_raw="${ENCLAVE_DEVCONTAINER_REWRITE_VARS:-}"
    local extra=""
    local var=""
    rewrite_home_var() {
        local var="$1"
        local val="${!var}"
        if [ -n "$val" ] && [[ "$val" == "$remote_home" || "$val" == "$remote_home/"* ]]; then
            export "$var"="${current_home}${val#"$remote_home"}"
            changed=1
        fi
    }
    rewrite_path_list_var() {
        local var="$1"
        local val="${!var}"
        if [ -z "$val" ]; then
            return
        fi
        # Split manually instead of IFS/read to preserve empty components
        # (leading/trailing or repeated colons) in PATH-like variables.
        local rest="$val"
        local piece=""
        local rebuilt=""
        local first=1
        local has_more=0
        local replaced=0
        while true; do
            if [[ "$rest" == *:* ]]; then
                piece="${rest%%:*}"
                rest="${rest#*:}"
                has_more=1
            else
                piece="$rest"
                rest=""
                has_more=0
            fi
            if [[ "$piece" == "$remote_home" || "$piece" == "$remote_home/"* ]]; then
                piece="${current_home}${piece#"$remote_home"}"
                replaced=1
            fi
            if [ "$first" = "1" ]; then
                rebuilt="$piece"
                first=0
            else
                rebuilt="${rebuilt}:$piece"
            fi
            if [ "$has_more" != "1" ]; then
                break
            fi
        done
        if [ "$replaced" = "1" ] && [ "$rebuilt" != "$val" ]; then
            export "$var"="$rebuilt"
            changed=1
        fi
    }
    if [ -n "$extra_raw" ]; then
        read -r -a extra_rewrite_vars <<< "${extra_raw//,/ }"
    fi
    for var in "${default_home_vars[@]}"; do
        rewrite_home_var "$var"
    done
    for extra in "${extra_rewrite_vars[@]}"; do
        if [[ "$extra" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
            rewrite_home_var "$extra"
        fi
    done
    # PATH-like variables are component-delimited; only rewrite exact home path
    # components to avoid substring collisions.
    for var in PATH NODE_PATH; do
        rewrite_path_list_var "$var"
    done
    # Intentionally double-pass extra vars:
    # - rewrite_home_var handles scalar paths
    # - rewrite_path_list_var handles colon-delimited path lists
    for extra in "${extra_rewrite_vars[@]}"; do
        if [[ "$extra" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
            rewrite_path_list_var "$extra"
        fi
    done
    if [ "$changed" = "1" ]; then
        echo "Normalized devcontainer paths from $remote_home to $current_home"
    fi
}

cleanup_devcontainer_once_stamps() {
    local stamp_root="$1"
    local label="$2"
    local current_stamp="$3"
    local stamp=""
    for stamp in "$stamp_root"/"$label"-*; do
        [ -e "$stamp" ] || continue
        if [ "$stamp" != "$current_stamp" ]; then
            rm -f "$stamp"
        fi
    done
}

execute_devcontainer_script() {
    local script="$1"
    local remote_user="${ENCLAVE_DEVCONTAINER_REMOTE_USER:-}"
    local current_user=""

    current_user="$(id -un 2>/dev/null || true)"
    if [ -n "$remote_user" ] && [ "$remote_user" != "$current_user" ]; then
        if id -u "$remote_user" >/dev/null 2>&1; then
            if command -v sudo >/dev/null 2>&1; then
                sudo -u "$remote_user" -- bash -l "$script"
                return $?
            fi
            if command -v su >/dev/null 2>&1; then
                su - "$remote_user" -c "bash -l '$script'"
                return $?
            fi
            echo "devcontainer remoteUser '$remote_user' requested but sudo/su is unavailable; running as ${current_user:-current user}"
        else
            echo "devcontainer remoteUser '$remote_user' not found in container; running as ${current_user:-current user}"
        fi
    fi

    bash -l "$script"
}

run_devcontainer_command() {
    local label="$1"
    local cmd="$2"
    local once="$3"
    local stamp_root=""
    local stamp_file=""
    local cmd_hash=""
    local script=""
    if [ -z "$cmd" ]; then
        return
    fi

    stamp_root="${ENCLAVE_TOOL_CONFIG_DIR:-$HOME}/.enclave-devcontainer"
    if [ "$once" = "1" ]; then
        mkdir -p "$stamp_root"
        cmd_hash="$(printf '%s' "$cmd" | sha256sum | cut -d' ' -f1)"
        stamp_file="$stamp_root/${label}-${cmd_hash}"
        cleanup_devcontainer_once_stamps "$stamp_root" "$label" "$stamp_file"
        if [ -f "$stamp_file" ]; then
            return
        fi
    fi

    echo "Running devcontainer ${label} command..."

    script="$(mktemp)"
    {
        printf '#!/usr/bin/env bash\n'
        if [ -n "$PROJECT_DIR" ]; then
            printf 'cd %q || exit 1\n' "$PROJECT_DIR"
        fi
        printf '%s\n' "$cmd"
    } > "$script"
    chmod 755 "$script"
    if ! execute_devcontainer_script "$script"; then
        echo "devcontainer ${label} command failed"
        if [ "${ENCLAVE_DEVCONTAINER_STRICT:-}" = "1" ]; then
            rm -f "$script"
            exit 1
        fi
    fi
    rm -f "$script"
    if [ "$once" = "1" ]; then
        touch "$stamp_file"
    fi
}

if [ "${ENCLAVE_DEVCONTAINER:-}" = "1" ]; then
    normalize_devcontainer_paths
    run_devcontainer_command "post-create" "${ENCLAVE_DEVCONTAINER_POST_CREATE:-}" "1"
    run_devcontainer_command "post-start" "${ENCLAVE_DEVCONTAINER_POST_START:-}" ""
fi

# Check if project has MCP servers and show reminder
if [ -n "$PROJECT_DIR" ] && { [ -f "$PROJECT_DIR/.mcp.json" ] || [ -f "$PROJECT_DIR/mcp.json" ]; }; then
    echo "MCP configuration detected. To enable MCP servers, see enclave documentation."
fi

# Set terminal for better experience
export TERM="${TERM:-xterm-256color}"
# Default to truecolor for modern terminals/agents unless explicitly overridden.
export COLORTERM="${COLORTERM:-truecolor}"

# Handle terminal size
if [ -t 0 ]; then
    # Update terminal size
    eval "$(resize 2>/dev/null || true)"
fi

# If running interactively, show welcome message
if [ -t 0 ] && [ -t 1 ]; then
    echo "enclave Development Environment"
    echo "--------------------------------"
    echo "Project Directory: ${PROJECT_DIR:-unknown}"
    python_info="Python: $(python3 --version 2>&1 | cut -d' ' -f2)"
    if command -v uv >/dev/null 2>&1; then
        python_info="$python_info (uv available)"
    fi
    echo "$python_info"
    echo "Node.js: $(node --version 2>/dev/null || echo 'not installed')"
    echo "Agent: ${agent_label^}"
    echo "--------------------------------"
    echo ""
fi

# Execute the command passed to docker run. When the session monitor is
# enabled, run it inside a managed tmux session (dedicated socket + config,
# isolated from any user ~/.tmux.conf) so `enclave status` can capture the
# rendered screen and OSC title from outside the container.
#
# Known trade-off: the tmux client exits 0 regardless of the wrapped command,
# so the container exit code no longer reflects the agent's. Propagating it
# would mean giving up exec (a shell waiting on a foreground tmux defers
# signal traps until the client exits, breaking prompt `docker stop`
# delivery) — a worse trade for interactive sessions, where the exit code is
# rarely consumed.
if [ "${ENCLAVE_SESSION_MONITOR:-}" = "1" ] && [ -t 0 ] && command -v tmux >/dev/null 2>&1; then
    exec tmux -f /usr/local/share/enclave/tmux-session.conf -L enclave new-session -s main -- "$@"
fi
exec "$@"
