#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

# Provisions the vm backend's base image inside a pristine Ubuntu cloud guest:
# the tool-only counterpart of the Dockerfile's system and tool-base stages,
# in the order microvm/alpine/build-bundle.sh installs a tool. Runs once, as
# root, from cloud-init, with the image build context mounted read-only.

set -euo pipefail

if [ "$#" -ne 4 ]; then
    echo "usage: provision.sh CONTEXT_DIR TOOL UID GID" >&2
    exit 2
fi

ctx="$1"
tool="$2"
uid="$3"
gid="$4"
user=agent
home="/home/$user"

case "$uid" in (*[!0-9]* | "") echo "provision.sh: UID must be numeric" >&2; exit 2 ;; esac
case "$gid" in (*[!0-9]* | "") echo "provision.sh: GID must be numeric" >&2; exit 2 ;; esac

export DEBIAN_FRONTEND=noninteractive
apt_get() { apt-get -o DPkg::Lock::Timeout=600 "$@"; }

# Background upgrades hold the apt lock while a session boots.
systemctl disable --now unattended-upgrades.service apt-daily.timer apt-daily-upgrade.timer >/dev/null 2>&1 || true

# The Dockerfile's system and tool-base package lists, plus what a VM session
# needs: sshd for the tool's terminal, iptables for loopback port forwards,
# dtach to keep a detached tool's terminal.
apt_get update
apt_get install -y --no-install-recommends \
    ca-certificates curl wget gnupg lsb-release sudo \
    git git-lfs bash locales tzdata \
    openssh-client openssh-server \
    zip unzip tar gzip bzip2 xz-utils \
    jq gettext-base \
    procps psmisc socat \
    ncurses-term bubblewrap \
    python3 python3-venv \
    direnv vim-tiny \
    build-essential gcc g++ make pkg-config \
    python3-dev libssl-dev libffi-dev dpkg-dev \
    iptables dtach
apt_get purge -y unattended-upgrades >/dev/null 2>&1 || true

# Read-only exports fall back to 9p on hosts whose virtiofsd cannot enforce
# read-only. The image's kernel modules include the 9p transport.
modprobe 9pnet_virtio 2>/dev/null ||
    echo "provision.sh: no 9p modules for $(uname -r); read-only mounts need a virtiofsd with --readonly" >&2

echo "en_US.UTF-8 UTF-8" > /etc/locale.gen
locale-gen

# yq and Node are pinned in the Dockerfile; read the pins from there so they
# cannot drift.
dockerfile_arg() {
    sed -n "s/^ARG $1=\(.*\)\$/\1/p" "$ctx/Dockerfile"
}

yq_version=$(dockerfile_arg YQ_VERSION)
if ! [[ "$yq_version" =~ ^v[0-9.]+$ ]]; then
    echo "provision.sh: no yq version in the Dockerfile's YQ_VERSION" >&2
    exit 1
fi
curl -fsSL "https://github.com/mikefarah/yq/releases/download/${yq_version}/yq_linux_amd64" -o /usr/local/bin/yq
chmod 0755 /usr/local/bin/yq
yq --version

# The Dockerfile copies Node out of its node image. A guest cannot, so it
# takes the latest release of the same major line and checks it against the
# release's checksum list.
node_image=$(dockerfile_arg AGENT_NODE_IMAGE)
node_major=${node_image#node:}
node_major=${node_major%%[!0-9]*}
if [ -z "$node_major" ] || [ "$node_image" = "${node_image#node:}" ]; then
    echo "provision.sh: no Node major version in the Dockerfile's AGENT_NODE_IMAGE" >&2
    exit 1
fi
node_dist="https://nodejs.org/dist/latest-v${node_major}.x"
node_sum=$(curl -fsSL "$node_dist/SHASUMS256.txt" | grep -E ' node-v[0-9.]+-linux-x64\.tar\.xz$')
node_file=${node_sum##* }
curl -fsSL "$node_dist/$node_file" -o "/tmp/$node_file"
(cd /tmp && printf '%s\n' "$node_sum" | sha256sum -c -)
# As the Dockerfile does, take the binary and its bundled npm only. The
# archive's bin/npm and bin/npx are links into npm, and the runtime helper
# below writes its wrappers to those paths.
node_unpacked=$(mktemp -d)
tar -xJf "/tmp/$node_file" -C "$node_unpacked" --strip-components=1 --no-same-owner
mkdir -p /opt/enclave/node/bin /opt/enclave/node/lib
cp "$node_unpacked/bin/node" /opt/enclave/node/bin/node
cp -R "$node_unpacked/lib/node_modules" /opt/enclave/node/lib/node_modules
rm -rf "$node_unpacked" "/tmp/$node_file"

# The tool user mirrors the host user: virtiofsd serves the shared
# directories with the host user's rights, whoever asks in the guest.
if existing=$(getent passwd "$uid" | cut -d: -f1) && [ -n "$existing" ]; then
    echo "provision.sh: UID $uid already belongs to $existing in the guest image" >&2
    exit 1
fi
if getent group "$gid" >/dev/null; then
    group=$(getent group "$gid" | cut -d: -f1)
else
    groupadd -g "$gid" "$user"
    group="$user"
fi
useradd -m -d "$home" -s /bin/bash -u "$uid" -g "$group" "$user"
echo "$user ALL=(root) NOPASSWD:/usr/bin/apt,/usr/bin/apt-get,/usr/bin/apt-cache,/usr/bin/apt-mark,/usr/bin/dpkg,/usr/bin/dpkg-deb,/usr/bin/dpkg-query" > "/etc/sudoers.d/$user"
chmod 0440 "/etc/sudoers.d/$user"

mkdir -p /opt/enclave/build-scripts /opt/enclave/extensions /usr/local/share/enclave /usr/share/doc/enclave
cp -R "$ctx/runtime-assets/build-scripts/." /opt/enclave/build-scripts/
cp -R "$ctx/extensions/tools" /opt/enclave/extensions/tools
if [ -d "$ctx/extensions/features" ]; then
    cp -R "$ctx/extensions/features" /opt/enclave/extensions/features
fi
if [ -d "$ctx/docs" ]; then
    cp -R "$ctx/docs" /usr/share/doc/enclave/docs
fi
install -m 0755 "$ctx/entrypoint.sh" /usr/local/bin/entrypoint.sh
install -m 0755 "$ctx/runtime-assets/auth-reconcile.sh" /usr/local/share/enclave/auth-reconcile.sh
install -m 0755 "$ctx/runtime-assets/net.sh" /usr/local/share/enclave/net.sh
install -m 0644 "$ctx/runtime-assets/kit-init.sh" /usr/local/share/enclave/kit-init.sh
chown -R root:root /opt/enclave /usr/share/doc/enclave
chmod -R a+rX /opt/enclave /usr/share/doc/enclave
find /opt/enclave/build-scripts -type f \( -name '*.sh' -o -path '*/bin/*' \) -exec chmod a+rx {} +
find /opt/enclave/extensions -type f -name install.sh -exec chmod a+rx {} +
ENCLAVE_AGENT_NODE_DIR=/opt/enclave/node /opt/enclave/build-scripts/install-agent-node-runtime.sh

cat > /tmp/enclave-install-tool.sh <<'INSTALL_TOOL'
#!/usr/bin/env bash
set -euo pipefail
tool="$1"
cd "$HOME"
export ENCLAVE_BUILD_SCRIPTS_DIR=/opt/enclave/build-scripts
export ENCLAVE_EXTENSIONS_ROOT=/opt/enclave/extensions
export ENCLAVE_AGENT_NODE_DIR=/opt/enclave/node
export PATH="$HOME/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
git config --global init.defaultBranch main
git config --global pull.rebase false
git lfs install
mkdir -p "$HOME/go/pkg/mod" "$HOME/go/pkg/sumdb" "$HOME/.cache/uv" "$HOME/.cache/go-build" \
    "$HOME/.local/bin" "$HOME/.config" "$HOME/.local/share"
/opt/enclave/build-scripts/install-agent-helper-bins.sh
enclave-install-tool "$tool" "$tool"
if [ -f /tmp/installed-tools.txt ]; then
    sort -u /tmp/installed-tools.txt > "$HOME/.installed-tools"
    rm -f /tmp/installed-tools.txt
fi
# Tool-only image: an empty manifest gates every feature's runtime hooks off.
# A missing one fails open in kit-init.sh.
: > "$HOME/.installed-features"
echo 'eval "$(direnv hook bash)"' >> "$HOME/.bashrc"
INSTALL_TOOL
chmod 0755 /tmp/enclave-install-tool.sh
runuser -u "$user" -- env HOME="$home" USER="$user" LOGNAME="$user" LANG=en_US.UTF-8 /tmp/enclave-install-tool.sh "$tool"
rm -f /tmp/enclave-install-tool.sh
/opt/enclave/build-scripts/install-tool-templates.sh

# The session side of the guest: a unit that fetches the host's boot script
# from the control export, and an sshd that accepts only the key the host
# puts on a tmpfs for each boot.
install -m 0755 "$ctx/runtime-assets/vm/base/enclave-vm-bootstrap" /usr/local/sbin/enclave-vm-bootstrap
install -m 0644 "$ctx/runtime-assets/vm/base/enclave-vm-boot.service" /etc/systemd/system/enclave-vm-boot.service
systemctl enable enclave-vm-boot.service
cat > /etc/ssh/sshd_config.d/10-enclave.conf <<'SSHD'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
AuthorizedKeysFile /run/enclave/authorized_keys
SSHD

# cloud-init wrote a network configuration bound to this boot's interface.
# Sessions run without cloud-init, so DHCP is configured for any interface.
rm -f /etc/netplan/50-cloud-init.yaml
cat > /etc/netplan/01-enclave.yaml <<'NETPLAN'
network:
  version: 2
  ethernets:
    all-en:
      match:
        name: "en*"
      dhcp4: true
NETPLAN
chmod 0600 /etc/netplan/01-enclave.yaml

# Leave nothing a project disk would inherit as identity or credential: no
# host keys, no machine id, no cloud-init run on the next boot.
touch /etc/cloud/cloud-init.disabled
rm -f /etc/ssh/ssh_host_*
truncate -s 0 /etc/machine-id
rm -f /var/lib/dbus/machine-id
apt-get clean
rm -rf /var/lib/apt/lists/*
