# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Enclave - Simplified multi-language development environment for agentic tools
# Base image: debian:trixie-slim (pinned) or ubuntu:24.04
ARG BASE_IMAGE=debian:trixie-slim@sha256:f6e2cfac5cf956ea044b4bd75e6397b4372ad88fe00908045e9a0d21712ae3ba
ARG DEVCONTAINER_BASE_IMAGE=0
ARG AGENT_NODE_IMAGE=node:24-trixie-slim@sha256:036dfa7e82a1e867b09248440a2b6635b3f8de557f69e60bac923a10c6e696a8
FROM ${BASE_IMAGE} AS system
ARG DEVCONTAINER_BASE_IMAGE
USER root

# Prevent interactive prompts during installation
ENV DEBIAN_FRONTEND=noninteractive
ENV LANG=en_US.UTF-8
ENV LANGUAGE=en_US:en
ENV LC_ALL=en_US.UTF-8

# Install core system dependencies
RUN if ! command -v apt-get >/dev/null 2>&1; then \
        echo "Base image must include apt-get (Debian/Ubuntu). Use --base-image with a Debian/Ubuntu image." >&2; \
        if [ "${DEVCONTAINER_BASE_IMAGE}" = "1" ]; then \
            echo "When using devcontainer mode, ensure the image in devcontainer.json is Debian/Ubuntu-based." >&2; \
        fi; \
        exit 1; \
    fi && \
    if [ ! -w /etc/apt/apt.conf.d ]; then \
        echo "/etc/apt/apt.conf.d is not writable; ensure the base image runs as root or override USER." >&2; \
        exit 1; \
    fi
# Explicit ids isolate these apt cache mounts from unrelated builds: anonymous
# cache mounts are keyed by target path and shared builder-wide, so another
# project's apt-get update could clobber our package lists (and vice versa).
RUN --mount=type=cache,id=enclave-apt-cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=enclave-apt-lib,target=/var/lib/apt,sharing=locked \
    rm -f /etc/apt/apt.conf.d/docker-clean && \
    echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/keep-cache && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        # Essentials
        ca-certificates curl wget gnupg lsb-release sudo \
        git git-lfs bash locales \
        openssh-client \
        # Archive tools
        zip unzip tar gzip bzip2 xz-utils \
        # JSON
        jq \
        # Templating (envsubst)
        gettext-base \
        # Process management
        procps psmisc socat \
        # Session monitor (terminal snapshots via `enclave status`); ncurses-term
        # ships the tmux-256color terminfo entry the managed session sets as TERM
        tmux ncurses-term \
        # Sandbox support
        bubblewrap \
        # Python runtime
        python3 python3-venv \
        # Environment management
        direnv \
        # Minimal editor
        vim-tiny && \
    # Setup locale
    echo "en_US.UTF-8 UTF-8" > /etc/locale.gen && \
    locale-gen

# Install yq (mikefarah/yq) for reading extension spec.yaml metadata during the
# build (feature/tool enablement, priority, needsRoot, aptPackages). Pinned like
# the other downloaded tools; jq above stays for agent-facing JSON handling.
ARG YQ_VERSION=v4.44.6
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
        amd64) yq_arch=amd64 ;; \
        arm64) yq_arch=arm64 ;; \
        *) echo "unsupported architecture for yq: $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${yq_arch}" -o /usr/local/bin/yq; \
    chmod 0755 /usr/local/bin/yq; \
    yq --version

# Create non-root user
ARG USER_ID=1000
ARG GROUP_ID=1000
ARG USERNAME=agent

RUN if id -u ${USER_ID} >/dev/null 2>&1; then \
        # UID exists (e.g. Ubuntu's "ubuntu" at 1000) - rename to our username
        EXISTING_USER=$(id -nu ${USER_ID}); \
        EXISTING_HOME=$(getent passwd "$EXISTING_USER" | cut -d: -f6); \
        if [ "$EXISTING_USER" != "${USERNAME}" ]; then \
            usermod -l ${USERNAME} -d /home/${USERNAME} -m ${EXISTING_USER}; \
            if [ -n "$EXISTING_HOME" ] && [ "$EXISTING_HOME" != "/home/${USERNAME}" ]; then \
                case "$EXISTING_HOME" in \
                    /home/*) \
                        if [ ! -e "$EXISTING_HOME" ]; then \
                            ln -s "/home/${USERNAME}" "$EXISTING_HOME"; \
                        fi ;; \
                esac; \
            fi; \
        fi; \
        usermod -s /bin/bash ${USERNAME}; \
        # Reconcile primary group to match GROUP_ID
        CURRENT_GID=$(id -g ${USERNAME}); \
        if [ "$CURRENT_GID" != "${GROUP_ID}" ]; then \
            if getent group ${GROUP_ID} >/dev/null 2>&1; then \
                groupmod -n ${USERNAME} $(getent group ${GROUP_ID} | cut -d: -f1); \
            else \
                groupadd -g ${GROUP_ID} ${USERNAME}; \
            fi; \
            usermod -g ${GROUP_ID} ${USERNAME}; \
        else \
            CURRENT_GNAME=$(id -gn ${USERNAME}); \
            if [ "$CURRENT_GNAME" != "${USERNAME}" ]; then \
                groupmod -n ${USERNAME} $CURRENT_GNAME; \
            fi; \
        fi; \
    else \
        # UID does not exist - create fresh user
        if getent group ${GROUP_ID} >/dev/null 2>&1; then \
            groupmod -n ${USERNAME} $(getent group ${GROUP_ID} | cut -d: -f1); \
        else \
            groupadd -g ${GROUP_ID} ${USERNAME}; \
        fi && \
        useradd -m -u ${USER_ID} -g ${GROUP_ID} -s /bin/bash ${USERNAME}; \
    fi && \
    echo "${USERNAME} ALL=(root) NOPASSWD:/usr/bin/apt,/usr/bin/apt-get,/usr/bin/apt-cache,/usr/bin/apt-mark,/usr/bin/dpkg,/usr/bin/dpkg-deb,/usr/bin/dpkg-query" > /etc/sudoers.d/${USERNAME} && \
    chmod 0440 /etc/sudoers.d/${USERNAME}

# Switch to user for language installations
USER ${USERNAME}
WORKDIR /home/${USERNAME}

# Ensure PATH for local installs
ENV PATH="/home/${USERNAME}/.local/bin:$PATH"

# Configure git
RUN git config --global init.defaultBranch main && \
    git config --global pull.rebase false && \
    git lfs install

FROM ${AGENT_NODE_IMAGE} AS agent-node-runtime

FROM system AS tool-base

ARG USER_ID=1000
ARG GROUP_ID=1000
ARG USERNAME=agent

USER root

# Install build deps needed for agent tools and features
RUN --mount=type=cache,id=enclave-apt-cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=enclave-apt-lib,target=/var/lib/apt,sharing=locked \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        build-essential gcc g++ make pkg-config \
        python3-dev libssl-dev libffi-dev \
        # Debian packaging tools
        debhelper dpkg-dev fakeroot lintian

# Install an agent-private Node runtime from the official Node image.
# Update AGENT_NODE_IMAGE to move the pinned Node runtime image/digest.
RUN mkdir -p /opt/enclave/node/bin /opt/enclave/node/lib
COPY --from=agent-node-runtime /usr/local/bin/node /opt/enclave/node/bin/node
COPY --from=agent-node-runtime /usr/local/lib/node_modules /opt/enclave/node/lib/node_modules
# Keep executable-asset rules here in sync with debian/rules,
# internal/appassets, internal/app/build_permissions.go, and internal/app/dockerfile_gen.go.
COPY runtime-assets/build-scripts /opt/enclave/build-scripts
RUN chmod -R a+rX /opt/enclave/build-scripts && \
    find /opt/enclave/build-scripts -type f \( -name '*.sh' -o -path '*/bin/*' \) -exec chmod a+rx {} +
RUN /opt/enclave/build-scripts/install-agent-node-runtime.sh

USER ${USERNAME}

# Global npm installs use a user-writable prefix via a COMMAND-SCOPED
# npm_config_prefix env var set by the build helpers (enclave-install-tool /
# enclave-agent-npm-install) and node-dev. We intentionally do NOT persist
# `prefix=` into ~/.npmrc here: nvm rejects a prefix/globalconfig in npmrc and
# aborts `nvm use` with exit 11, which would break node-dev images at build and
# at container startup.

# Pre-create user-owned directories before cache mounts.
# BuildKit may create parent mount paths as root, which breaks sibling writes.
RUN mkdir -p "$HOME/go/pkg/mod" "$HOME/go/pkg/sumdb" "$HOME/.cache/uv" "$HOME/.cache/go-build"

RUN mkdir -p "$HOME/.local/bin" "$HOME/.config" "$HOME/.cache" "$HOME/.local/share"
RUN /opt/enclave/build-scripts/install-agent-helper-bins.sh

FROM tool-base AS feature-base

ARG USER_ID=1000
ARG GROUP_ID=1000
ARG USERNAME=agent
ARG FEATURES=default

USER root

# BEGIN ENCLAVE_FEATURE_INSTALLS
# NOTE: This block is replaced at build time by app/dockerfile_gen.go with one
# source-copy + install block per selected feature (priority ordered), so adding
# or changing one feature does not re-run the others' copies, apt installs, or
# scripts. The aggregated fallback below supports direct "docker build ." for
# developer testing but without per-feature layer caching.
COPY extensions/features /opt/enclave/extensions/features
RUN chmod -R a+rX /opt/enclave/extensions/features && \
    find /opt/enclave/extensions/features -type f -name install.sh -exec chmod a+rx {} +

# Dynamically install apt packages from feature extensions
# Aggregates aptPackages from all enabled feature spec.yaml files
RUN --mount=type=cache,id=enclave-apt-cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=enclave-apt-lib,target=/var/lib/apt,sharing=locked \
    FEATURES="${FEATURES}" /opt/enclave/build-scripts/install-feature-apt-packages.sh

# Run feature install scripts that require root (needsRoot: true)
# These are sorted by priority (lower first)
RUN FEATURES="${FEATURES}" \
    ENCLAVE_FEATURE_PHASE=root \
    /opt/enclave/build-scripts/run-feature-installs.sh

USER ${USERNAME}

# Run feature install scripts that do not require root (needsRoot: false or not set)
# These are sorted by priority (lower first)
RUN --mount=type=cache,id=enclave-npm-${USER_ID},target=/home/${USERNAME}/.npm,uid=${USER_ID},gid=${GROUP_ID} \
    --mount=type=cache,id=enclave-gomod-${USER_ID},target=/home/${USERNAME}/go/pkg/mod,uid=${USER_ID},gid=${GROUP_ID} \
    --mount=type=cache,id=enclave-uv-${USER_ID},target=/home/${USERNAME}/.cache/uv,uid=${USER_ID},gid=${GROUP_ID} \
    FEATURES="${FEATURES}" \
    ENCLAVE_FEATURE_PHASE=user \
    /opt/enclave/build-scripts/run-feature-installs.sh
# END ENCLAVE_FEATURE_INSTALLS

# Persist the resolved enabled-feature set so the entrypoint can gate per-feature
# runtime hooks (initFiles/workspace/startup) to selected features only. Mirrors
# $HOME/.installed-tools. Placed outside the FEATURE_INSTALLS marker block so it
# survives dockerfile_gen.go's per-feature rewrite. Non-fatal: if the manifest
# cannot be written the entrypoint gate fails open (applies to all baked
# features, the prior behavior). Runs under bash (common.sh uses bashisms) with
# pipefail, staging through a temp file so a failed pipeline leaves the manifest
# ABSENT (fail-open), never empty (which would gate every feature hook off).
RUN bash -o pipefail -c '. /opt/enclave/build-scripts/lib/common.sh && \
    enclave_list_enabled_features "${FEATURES-default}" | cut -f2 | sort -u \
        > "$HOME/.installed-features.tmp" && \
    mv "$HOME/.installed-features.tmp" "$HOME/.installed-features"' || \
    rm -f "$HOME/.installed-features.tmp"

# BEGIN ENCLAVE_TOOL_INSTALLS
# NOTE: This block is replaced at build time by app/dockerfile_gen.go with
# per-tool COPY, cache-busting stamps, and a standard stage definition.
# The fallback below supports direct "docker build ." for developer testing
# but without per-tool layer caching.
FROM feature-base AS standard
ARG USER_ID=1000
ARG GROUP_ID=1000
ARG USERNAME=agent
ARG AGENT_TOOLS=all
USER root
COPY extensions/tools /opt/enclave/extensions/tools
RUN chmod -R a+rX /opt/enclave/extensions/tools && \
    find /opt/enclave/extensions/tools -type f -name install.sh -exec chmod a+rx {} +
USER ${USERNAME}
RUN bash -c "set -e; \
    for script in /opt/enclave/extensions/tools/*/install.sh; do \
        if [ -x \"\$script\" ]; then \
            tool=\$(dirname \"\$script\" | xargs basename); \
            enclave-install-tool \"\$tool\" \"\${AGENT_TOOLS}\"; \
        fi; \
    done"
RUN if [ -f /tmp/installed-tools.txt ]; then \
        sort -u /tmp/installed-tools.txt > $HOME/.installed-tools; \
        rm /tmp/installed-tools.txt; \
    fi
# END ENCLAVE_TOOL_INSTALLS

# Install tool configuration templates (lightweight file copy, after tool stages merge)
USER root
COPY extensions/tools /opt/enclave/extensions/tools
RUN chmod -R a+rX /opt/enclave/extensions/tools && \
    find /opt/enclave/extensions/tools -type f -name install.sh -exec chmod a+rx {} +
RUN /opt/enclave/build-scripts/install-tool-templates.sh

# Stage a pristine, agent-owned copy of the extension trees for the files/home
# bake. The working copies under /opt/enclave/extensions were chmod'd a+rX for
# agent traversal, which widens restrictive files/home modes; COPY preserves the
# source modes and --chown gives the agent ownership so it can read them during
# the bake, so a kit's files/home lands with its declared mode intact.
COPY --chown=${USERNAME}:${USERNAME} extensions/features /opt/enclave/home-files-src/features
COPY --chown=${USERNAME}:${USERNAME} extensions/tools /opt/enclave/home-files-src/tools

# Bake extension files/home trees into the agent home (kit wins on overwrite).
# Runs as the agent user so the baked files are agent-owned; only enabled
# features and included tools are materialized. FEATURES is not carried by the
# generated standard-stage ARGs, so re-declare it (and AGENT_TOOLS) here.
ARG FEATURES=default
ARG AGENT_TOOLS=all
USER ${USERNAME}
RUN FEATURES="${FEATURES}" AGENT_TOOLS="${AGENT_TOOLS}" \
    ENCLAVE_HOME_FILES_SRC=/opt/enclave/home-files-src \
    /opt/enclave/build-scripts/install-extension-home-files.sh
USER root

# Bundle documentation for in-container agent help.
COPY docs/ /usr/share/doc/enclave/docs/
RUN for readme in /opt/enclave/extensions/tools/*/README.md; do \
        [ -f "$readme" ] || continue; \
        tool=$(basename "$(dirname "$readme")"); \
        mkdir -p "/usr/share/doc/enclave/extensions/tools/$tool"; \
        cp "$readme" "/usr/share/doc/enclave/extensions/tools/$tool/README.md"; \
    done; \
    for readme in /opt/enclave/extensions/features/*/README.md; do \
        [ -f "$readme" ] || continue; \
        feat=$(basename "$(dirname "$readme")"); \
        mkdir -p "/usr/share/doc/enclave/extensions/features/$feat"; \
        cp "$readme" "/usr/share/doc/enclave/extensions/features/$feat/README.md"; \
    done; \
    chmod -R a+rX /usr/share/doc/enclave
USER ${USERNAME}

# Setup direnv hooks for automatic .envrc loading (always needed for shell integration)
RUN echo 'eval "$(direnv hook bash)"' >> ~/.bashrc && \
    if [ -f ~/.zshrc ]; then echo 'eval "$(direnv hook zsh)"' >> ~/.zshrc; fi

# Copy entrypoint last — most volatile layer
USER root
COPY runtime-assets/auth-reconcile.sh /usr/local/share/enclave/auth-reconcile.sh
COPY runtime-assets/net.sh /usr/local/share/enclave/net.sh
COPY runtime-assets/tmux-session.conf /usr/local/share/enclave/tmux-session.conf
COPY runtime-assets/kit-init.sh /usr/local/share/enclave/kit-init.sh
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod a+rx /usr/local/bin/entrypoint.sh \
        /usr/local/share/enclave/auth-reconcile.sh \
        /usr/local/share/enclave/net.sh && \
    chmod a+r /usr/local/share/enclave/tmux-session.conf
USER ${USERNAME}
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["/bin/bash"]
