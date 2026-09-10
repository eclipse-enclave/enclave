#!/usr/bin/env bash
# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

set -euo pipefail

if [ "$#" -ne 5 ]; then
    echo "usage: build-bundle.sh OUTPUT_DIR REPO_ROOT TOOL UID GID" >&2
    exit 2
fi

output_dir="$1"
repo_root="$2"
tool="$3"
uid="$4"
gid="$5"

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$repo_root" && pwd)
output_dir=$(mkdir -p "$output_dir" && cd "$output_dir" && pwd)

alpine_image="${ENCLAVE_QEMU_ALPINE_IMAGE:-alpine@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659}"
kernel_package="${ENCLAVE_QEMU_KERNEL_PACKAGE:-linux-virt}"

if ! command -v docker >/dev/null 2>&1; then
    echo "build-bundle.sh: docker is required to build qemu bundles" >&2
    exit 1
fi

case "$uid" in (*[!0-9]* | "") echo "build-bundle.sh: UID must be numeric" >&2; exit 2 ;; esac
case "$gid" in (*[!0-9]* | "") echo "build-bundle.sh: GID must be numeric" >&2; exit 2 ;; esac
case "$kernel_package" in (*[!A-Za-z0-9_.+-]* | "") echo "build-bundle.sh: ENCLAVE_QEMU_KERNEL_PACKAGE must be a package name" >&2; exit 2 ;; esac

echo "build-bundle.sh: using Alpine kernel package $kernel_package" >&2

build_image="enclave-microvm-bundle-build:$$-$RANDOM"
build_cid=""
cleanup() {
    if [ -n "$build_cid" ]; then
        docker rm -f "$build_cid" >/dev/null 2>&1 || true
    fi
    docker rmi -f "$build_image" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run --rm -i --platform linux/amd64 \
    -e "ENCLAVE_QEMU_TOOL=$tool" \
    -e "ENCLAVE_QEMU_UID=$uid" \
    -e "ENCLAVE_QEMU_GID=$gid" \
    -e "ENCLAVE_QEMU_KERNEL_PACKAGE=$kernel_package" \
    -v "$repo_root:/src:ro" \
    -v "$script_dir:/microvm:ro" \
    "$alpine_image" \
    /bin/sh -eu <<'PHASE1' | docker import --platform linux/amd64 - "$build_image" >/dev/null
set -eu
exec 3>&1 1>&2

apk add --no-cache tar >/dev/null

root=/tmp/rootfs
mkdir -p "$root"

# yq-go (mikefarah yq v4) and gettext-envsubst (envsubst) back the spec.yaml
# reads in the build scripts and kit-init.sh's runtime initFiles seeding — the
# same contract the Docker image satisfies with its pinned yq and gettext-base.
apk --root "$root" --initdb --arch x86_64 --keys-dir /etc/apk/keys --repositories-file /etc/apk/repositories add --no-cache \
    alpine-base "$ENCLAVE_QEMU_KERNEL_PACKAGE" bash busybox-suid ca-certificates curl wget git git-lfs openssh-client sudo \
    jq yq-go gettext-envsubst nodejs npm python3 py3-pip coreutils findutils shadow util-linux setpriv kmod iproute2 iptables \
    procps psmisc socat direnv vim zip unzip tar gzip bzip2 xz >/dev/null

mkdir -p "$root/dev" "$root/proc" "$root/sys" "$root/tmp"
chmod 1777 "$root/tmp"
mknod -m 666 "$root/dev/null" c 1 3 2>/dev/null || true
mknod -m 666 "$root/dev/zero" c 1 5 2>/dev/null || true
mknod -m 666 "$root/dev/random" c 1 8 2>/dev/null || true
mknod -m 666 "$root/dev/urandom" c 1 9 2>/dev/null || true
cp /etc/resolv.conf "$root/etc/resolv.conf"
cp /microvm/init "$root/init"
chmod 0755 "$root/init"

chroot "$root" /bin/sh -eu -c '
    gid="$1"
    uid="$2"
    if ! getent group "$gid" >/dev/null 2>&1; then
        addgroup -g "$gid" agent
    fi
    group_name=$(getent group "$gid" | cut -d: -f1)
    if ! id -u agent >/dev/null 2>&1; then
        adduser -D -h /home/agent -s /bin/bash -u "$uid" -G "$group_name" agent
    fi
    mkdir -p /etc/sudoers.d
    echo "agent ALL=(root) NOPASSWD:/sbin/apk,/usr/bin/apk" > /etc/sudoers.d/agent
    chmod 0440 /etc/sudoers.d/agent
' sh "$ENCLAVE_QEMU_GID" "$ENCLAVE_QEMU_UID"

mkdir -p \
    "$root/opt/enclave/build-scripts" \
    "$root/opt/enclave/extensions" \
    "$root/opt/enclave/node/bin" \
    "$root/usr/local/bin" \
    "$root/usr/local/share/enclave" \
    "$root/usr/share/doc/enclave"
cp -a /src/runtime-assets/build-scripts/. "$root/opt/enclave/build-scripts/"
cp -a /src/extensions/tools "$root/opt/enclave/extensions/tools"
if [ -d /src/extensions/features ]; then
    cp -a /src/extensions/features "$root/opt/enclave/extensions/features"
fi
cp /src/entrypoint.sh "$root/usr/local/bin/entrypoint.sh"
cp /src/runtime-assets/auth-reconcile.sh "$root/usr/local/share/enclave/auth-reconcile.sh"
cp /src/runtime-assets/net.sh "$root/usr/local/share/enclave/net.sh"
cp /src/runtime-assets/kit-init.sh "$root/usr/local/share/enclave/kit-init.sh"
chmod 0755 "$root/usr/local/bin/entrypoint.sh" "$root/usr/local/share/enclave/auth-reconcile.sh" "$root/usr/local/share/enclave/net.sh"
chmod 0644 "$root/usr/local/share/enclave/kit-init.sh"

if [ -d /src/docs ]; then cp -a /src/docs "$root/usr/share/doc/enclave/docs"; fi

ln -sf /usr/bin/node "$root/opt/enclave/node/bin/node"
ln -sf /usr/bin/npm "$root/opt/enclave/node/bin/npm"

chroot "$root" /bin/sh -eu -c '
    chown -R agent:$(id -gn agent) /home/agent /opt/enclave /usr/share/doc/enclave
    chmod -R a+rX /opt/enclave/extensions /usr/share/doc/enclave
'

cat > "$root/tmp/enclave-install-tool.sh" <<INSTALL_TOOL
#!/usr/bin/env bash
set -euo pipefail
export HOME=/home/agent
cd "\$HOME"
export ENCLAVE_BUILD_SCRIPTS_DIR=/opt/enclave/build-scripts
export ENCLAVE_EXTENSIONS_ROOT=/opt/enclave/extensions
export ENCLAVE_AGENT_NODE_DIR=/opt/enclave/node
export PATH=/opt/enclave/node/bin:\$HOME/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
/opt/enclave/build-scripts/install-agent-helper-bins.sh
enclave-install-tool "${ENCLAVE_QEMU_TOOL}" "${ENCLAVE_QEMU_TOOL}"
if [ -f /tmp/installed-tools.txt ]; then
    sort -u /tmp/installed-tools.txt > "\$HOME/.installed-tools"
    rm -f /tmp/installed-tools.txt
fi
# Tool-only bundle: no features are installed, so bake an EMPTY enabled-features
# manifest. An ABSENT manifest fails open in kit-init.sh and the entrypoint
# would run runtime hooks for every feature dir baked into the rootfs; an empty
# one gates them all off.
: > "\$HOME/.installed-features"
INSTALL_TOOL
chmod 0755 "$root/tmp/enclave-install-tool.sh"

cat > "$root/tmp/enclave-provision-rootfs.sh" <<'PHASE2'
#!/bin/sh
set -eu
/bin/su -s /bin/bash -c /tmp/enclave-install-tool.sh agent

/opt/enclave/build-scripts/install-tool-templates.sh

echo 'eval "$(direnv hook bash)"' >> /home/agent/.bashrc

# Package-manager download caches are build-time only. The Docker image keeps
# them out of its layers with BuildKit cache mounts; here they would be baked
# into the rootfs, and every rootfs byte costs a guest RAM byte because the
# initramfs is unpacked into a ramfs that is never reclaimed.
rm -rf \
    /home/agent/.npm \
    /home/agent/.cache/pip \
    /home/agent/.cache/uv \
    /home/agent/.cache/go-build \
    /root/.npm \
    /root/.cache

rm -f /tmp/enclave-install-tool.sh /tmp/enclave-provision-rootfs.sh
PHASE2
chmod 0755 "$root/tmp/enclave-provision-rootfs.sh"

tar -C "$root" -cf - . >&3
PHASE1

build_cid=$(docker create --platform linux/amd64 "$build_image" /bin/sh -eu /tmp/enclave-provision-rootfs.sh)
docker start -a "$build_cid" || true
status=$(docker wait "$build_cid")
if [ "$status" != "0" ]; then
    echo "build-bundle.sh: tool install failed (exit $status)" >&2
    exit 1
fi

# shellcheck disable=SC2016 # expanded by the child shell, not while assigning here.
phase3_script='
set -eu
apk add --no-cache cpio kmod tar zstd >/dev/null

root=/tmp/rootfs
mkdir -p "$root"
tar -C "$root" -xf -
rm -f "$root/.dockerenv"

# Docker bind-mounts /etc/hosts into the provisioning container, so the export
# carries an empty placeholder and the guest ends up with no loopback entry at
# all: resolving "localhost" falls through to musl built-ins, which answer ::1
# first. /etc/resolv.conf is a placeholder for the same reason but udhcpc
# rewrites it during startup.
cat > "$root/etc/hosts" <<HOSTS
127.0.0.1	localhost localhost.localdomain
::1	localhost ip6-localhost ip6-loopback
HOSTS

# docker export leaves runtime placeholders in /dev (a regular-file console
# would swallow all guest output) and node/npm scratch in /tmp (init mounts a
# tmpfs over it, so the content is pure initramfs bloat). Rebuild both fresh.
rm -rf "$root/dev" "$root/tmp"
mkdir -m 755 "$root/dev"
mkdir -m 1777 "$root/tmp"
mkdir -p "$root/dev/pts" "$root/dev/shm"
mknod -m 600 "$root/dev/console" c 5 1
mknod -m 666 "$root/dev/null" c 1 3
mknod -m 666 "$root/dev/zero" c 1 5
mknod -m 666 "$root/dev/random" c 1 8
mknod -m 666 "$root/dev/urandom" c 1 9

kernel=$(find "$root/boot" -maxdepth 1 -type f -name "vmlinuz-*" | head -n1)
if [ -z "$kernel" ]; then
    echo "missing guest kernel under $root/boot" >&2
    exit 1
fi
cp "$kernel" /out/vmlinuz

# Drop the kernel-image symlinks shipped inside the modules directories; their
# absolute /boot targets cannot resolve here and depmod complains about them.
rm -f "$root"/lib/modules/*/vmlinuz*
kernel_version=$(find "$root/lib/modules" -mindepth 1 -maxdepth 1 -type d | sed "s#^.*/##" | head -n1)
if [ -n "$kernel_version" ]; then
    depmod -a -b "$root" "$kernel_version"
fi
# Nothing in the guest reads /boot: it boots the kernel the host hands to QEMU,
# so the Alpine kernel image, its initramfs, System.map and config would only
# occupy guest RAM.
rm -rf "$root/boot"
mkdir -m 755 "$root/boot"

# Compress the image: the kernel holds the initrd and the fully unpacked ramfs
# in memory at the same time, so an uncompressed archive costs its own size
# twice at boot. zstd is both smaller and faster to unpack than gzip here.
initramfs=/out/initramfs.cpio.zst
( cd "$root" && find . -print | cpio -o -H newc --quiet ) | zstd -q -f -T0 -9 -o "$initramfs"
zstd -q -t "$initramfs"
uncompressed=$(zstd -q -d -c "$initramfs" | wc -c)
if [ "$uncompressed" -le 0 ]; then
    echo "empty guest initramfs" >&2
    exit 1
fi

# unpack_to_rootfs() only takes an appended plain cpio segment - the host-side
# run overlay - when it starts on a 4-byte boundary, so pad the compressed
# image out to one. The kernel skips the padding bytes before the segment.
compressed=$(wc -c < "$initramfs")
pad=$(( (4 - compressed % 4) % 4 ))
if [ "$pad" -gt 0 ]; then
    dd if=/dev/zero bs=1 count="$pad" >> "$initramfs" 2>/dev/null
    compressed=$(wc -c < "$initramfs")
fi

# The host sizes guest memory from the unpacked size: the initramfs becomes the
# rootfs, a ramfs the guest can never reclaim.
printf "{\n  \"uncompressedBytes\": %s,\n  \"compressedBytes\": %s,\n  \"compression\": \"zstd\"\n}\n" \
    "$uncompressed" "$compressed" > /out/enclave-vm-initramfs.json
'
docker export "$build_cid" | docker run --rm -i --platform linux/amd64 \
    -v "$output_dir:/out" \
    "$alpine_image" \
    /bin/sh -euc "$phase3_script"

echo "built qemu alpine microvm bundle at $output_dir" >&2
