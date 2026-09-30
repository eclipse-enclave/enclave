#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

: "${ENCLAVE_EXTENSIONS_ROOT:=/opt/enclave/extensions}"
: "${ENCLAVE_FEATURES_DIR:=${ENCLAVE_EXTENSIONS_ROOT}/features}"
: "${ENCLAVE_TOOLS_DIR:=${ENCLAVE_EXTENSIONS_ROOT}/tools}"
: "${ENCLAVE_AGENT_NODE_DIR:=/opt/enclave/node}"
: "${ENCLAVE_BUILD_SCRIPTS_DIR:=/opt/enclave/build-scripts}"
: "${ENCLAVE_TEMPLATES_DIR:=/usr/local/share/enclave/templates}"
: "${ENCLAVE_INSTALLED_TOOLS_FILE:=/tmp/installed-tools.txt}"

enclave_require_command() {
    local cmd="$1"
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "missing required command: $cmd" >&2
        exit 1
    fi
}

enclave_require_dir() {
    local dir="$1"
    if [ ! -d "$dir" ]; then
        echo "missing required directory: $dir" >&2
        exit 1
    fi
}

enclave_load_nvm_no_use() {
    local nvm_dir="${1:-${NVM_DIR:-$HOME/.nvm}}"
    if [ ! -s "$nvm_dir/nvm.sh" ]; then
        return 1
    fi
    export NVM_DIR="$nvm_dir"
    # Load nvm WITHOUT auto-activating: a bare source implicitly runs
    # `nvm use default`, which can return non-zero (exit 11 on an ~/.npmrc
    # prefix conflict, exit 3 on a version cache miss) and abort callers running
    # under `set -e`. --no-use loads functions only; callers can activate the
    # desired version explicitly and non-fatally.
    # shellcheck source=/dev/null
    . "$NVM_DIR/nvm.sh" --no-use || true
}

# enclave_ext_spec echoes the path to an extension's spec manifest
# (spec.yaml, falling back to spec.json) inside the given extension directory,
# or nothing when neither exists. The build reads extension metadata straight
# from the Enclave runtime capability (or legacy v1) via yq.
enclave_ext_spec() {
    local dir="$1"
    if [ -f "$dir/spec.yaml" ]; then
        echo "$dir/spec.yaml"
    elif [ -f "$dir/spec.json" ]; then
        echo "$dir/spec.json"
    fi
}

# enclave_spec_read evaluates a yq expression against a spec manifest and
# echoes the result. When the manifest is missing it echoes the caller-supplied
# fallback so callers get the same default the old jq null-checks provided.
enclave_spec_read() {
    local spec="$1"
    local expr="$2"
    local fallback="${3-}"
    if [ -z "$spec" ] || [ ! -f "$spec" ]; then
        printf '%s' "$fallback"
        return 0
    fi
    yq '((. | select(.schemaVersion != "3")), (.capabilities[] | select(.type == "org.eclipse.enclave/runtime@1") | .config)) | '"$expr" "$spec"
}

# enclave_copy_home_files bakes an extension's files/home tree into $HOME,
# preserving mode and overwriting existing files (kit wins). The trailing "/."
# copies the directory contents (including dotfiles) and merges subdirectories
# into any existing $HOME layout. No-op when files/home is absent.
#
# When ENCLAVE_HOME_FILES_SRC is set (build time), read files/home from a
# pristine, agent-owned copy of the extension tree rooted there instead of the
# working copy under /opt/enclave/extensions, which was chmod'd a+rX for agent
# traversal and would widen restrictive modes (e.g. 0640 -> 0644). Derives the
# kit's kind+name from ext_dir (.../extensions/{features,tools}/<name>). Falls
# back to the working copy when the pristine tree or env var is absent, so plain
# `docker build` and the unit test still work.
enclave_copy_home_files() {
    local ext_dir="$1"
    local home_src="$ext_dir/files/home"
    if [ -n "${ENCLAVE_HOME_FILES_SRC:-}" ]; then
        local name kind pristine
        name="$(basename "$ext_dir")"
        kind="$(basename "$(dirname "$ext_dir")")"
        pristine="$ENCLAVE_HOME_FILES_SRC/$kind/$name/files/home"
        [ -d "$pristine" ] && home_src="$pristine"
    fi
    [ -d "$home_src" ] || return 0
    mkdir -p "$HOME"
    cp -R --preserve=mode "$home_src/." "$HOME/"
}

# enclave_is_root_user reports whether a spec command's user field is root.
enclave_is_root_user() {
    case "${1:-}" in
        0 | root) return 0 ;;
        *) return 1 ;;
    esac
}

# enclave_run_install_command <user> <cmd> [args...]
# Runs a commands.install argv. When the current process is root AND the
# command targets a non-root user, drop privileges via
# ${ENCLAVE_SUDO:-sudo} -u ${ENCLAVE_AGENT_USER:-agent}. Otherwise run
# directly (agent-phase RUNs already execute as the sandbox user). Returns the
# command's exit status so callers can honor failOnInstallError. No-op with no
# command argv.
enclave_run_install_command() {
    local user="$1"
    shift
    [ "$#" -ge 1 ] || return 0
    if [ "$(id -u)" -eq 0 ] && ! enclave_is_root_user "$user"; then
        "${ENCLAVE_SUDO:-sudo}" -u "${ENCLAVE_AGENT_USER:-agent}" -- "$@"
    else
        "$@"
    fi
}

enclave_word_list_contains() {
    local haystack="${1:-}"
    local needle="${2:-}"
    local token=""
    for token in $haystack; do
        if [ "$token" = "$needle" ]; then
            return 0
        fi
    done
    return 1
}

enclave_feature_is_enabled() {
    local spec="$1"
    local feature_name="$2"
    local selection="${3-default}"

    if [ -z "$selection" ]; then
        return 1
    fi

    # Explicitly listed by name — always enabled
    if enclave_word_list_contains "$selection" "$feature_name"; then
        return 0
    fi

    # "all" keyword — every feature regardless of defaultEnabled
    if [ "$selection" = "all" ] || enclave_word_list_contains "$selection" "all"; then
        return 0
    fi

    # "default" keyword — only features with defaultEnabled true/omitted.
    # `!= false` treats an absent (null) key as enabled while honoring an
    # explicit `defaultEnabled: false`, matching the retired jq null-check.
    if [ "$selection" = "default" ] || enclave_word_list_contains "$selection" "default"; then
        [ "$(enclave_spec_read "$spec" '.defaultEnabled != false')" = "true" ]
        return
    fi

    return 1
}

enclave_tool_is_enabled() {
    local spec="$1"
    local tool_name="$2"
    local selection="${3-all}"

    if [ -z "$selection" ]; then
        return 1
    fi

    # "all" keyword — default-included tools only
    if [ "$selection" = "all" ]; then
        if [ -z "$spec" ] || [ ! -f "$spec" ]; then
            return 0
        fi
        [ "$(enclave_spec_read "$spec" '.defaultIncluded != false')" = "true" ]
        return
    fi

    enclave_word_list_contains "$selection" "$tool_name"
}

# Local associative arrays in the caller scope are shared by recursive visits.
enclave_visit_feature() {
    local name="$1" spec dependency priority
    if [[ ! "$name" =~ ^[a-z0-9][a-z0-9._-]*$ ]]; then echo "Invalid required feature name: $name" >&2; return 1; fi
    case "${feature_states[$name]:-}" in
        visiting) echo "Feature dependency cycle at: $name" >&2; return 1 ;;
        done) return 0 ;;
    esac
    spec="$(enclave_ext_spec "$ENCLAVE_FEATURES_DIR/$name")"
    if [ -z "$spec" ]; then
        echo "Required feature is unavailable: $name" >&2
        return 1
    fi
    feature_states[$name]=visiting
    local dependencies
    dependencies="$(enclave_spec_read "$spec" '.requiresFeatures[]' '')" || return
    while IFS= read -r dependency; do
        [ -n "$dependency" ] || continue
        enclave_visit_feature "$dependency" || return
    done <<< "$dependencies"
    feature_states[$name]="done"
    priority="$(enclave_spec_read "$spec" '.priority // 100')"
    printf '%s\t%s\t%s\n' "$priority" "$name" "$ENCLAVE_FEATURES_DIR/$name"
}

enclave_list_enabled_features() {
    local selection="${1-default}" ext spec name priority
    local -A feature_states=()
    local roots="" dependencies="" tool_dir
    enclave_require_command yq
    for ext in "$ENCLAVE_FEATURES_DIR"/*/; do
        [ -d "$ext" ] || continue
        spec="$(enclave_ext_spec "${ext%/}")"
        [ -n "$spec" ] || continue
        name="$(basename "$ext")"
        enclave_feature_is_enabled "$spec" "$name" "$selection" || continue
        priority="$(enclave_spec_read "$spec" '.priority // 100')"
        roots+="$priority"$'\t'"$name"$'\n'
    done
    if [ "${ENCLAVE_FEATURES_RESOLVED:-0}" = 1 ]; then
        while IFS=$'\t' read -r priority name; do
            [ -n "$name" ] || continue
            printf '%s\t%s\t%s\n' "$priority" "$name" "$ENCLAVE_FEATURES_DIR/$name"
        done <<< "$roots"
        return
    fi
    # Direct Docker builds resolve the selected tools' requirements here. The
    # CLI supplies the already resolved feature set and stages only that closure.
    for tool_dir in "$ENCLAVE_EXTENSIONS_ROOT"/tools/*/; do
        [ -d "$tool_dir" ] || continue
        spec="$(enclave_ext_spec "${tool_dir%/}")"
        [ -n "$spec" ] || continue
        name="$(basename "$tool_dir")"
        enclave_tool_is_enabled "$spec" "$name" "${AGENT_TOOLS:-}" || continue
        dependencies="$(enclave_spec_read "$spec" '.requiresFeatures[]' '')" || return
        while IFS= read -r name; do
            [ -n "$name" ] || continue
            roots+="0"$'\t'"$name"$'\n'
        done <<< "$dependencies"
    done
    while IFS=$'\t' read -r priority name; do
        [ -n "$name" ] || continue
        enclave_visit_feature "$name" || return
    done < <(printf '%s' "$roots" | sort -n -k1,1 -k2,2)
}

enclave_list_feature_installers() {
    local selection="${1-default}"
    local phase="${2:-user}"
    local priority=""
    local name=""
    local ext=""
    local spec=""
    local needs_root=""
    local script=""

    while IFS=$'\t' read -r priority name ext; do
        spec="$(enclave_ext_spec "$ext")"
        needs_root="$(enclave_spec_read "$spec" '.needsRoot // false' false)"

        case "$phase" in
            root)
                [ "$needs_root" = "true" ] || continue
                ;;
            user)
                [ "$needs_root" = "true" ] && continue
                ;;
            *)
                echo "invalid feature install phase: $phase" >&2
                exit 1
                ;;
        esac

        script="$ext/install.sh"
        [ -x "$script" ] || continue
        printf '%s\t%s\t%s\n' "$priority" "$name" "$script"
    done < <(enclave_list_enabled_features "$selection")
}
