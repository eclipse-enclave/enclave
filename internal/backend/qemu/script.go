// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package qemu

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/model"
	"enclave/internal/util"
)

func (b *Backend) buildQEMUArgs(bundle bundle, runtime guestRuntime, req backend.Request) ([]string, error) {
	netdevArg := "user,id=" + qemuNetdevID
	for _, port := range req.Ports {
		hostfwd, err := formatHostfwd(port)
		if err != nil {
			return nil, err
		}
		if err := rejectQEMUOptionComma("host port forwarding", hostfwd); err != nil {
			return nil, err
		}
		netdevArg += ",hostfwd=" + hostfwd
	}
	name := qemuName(req)
	if err := rejectQEMUOptionComma("session name", name); err != nil {
		return nil, err
	}
	transport := qemuTransport()
	args := []string{
		"-nodefaults",
		"-no-user-config",
		"-machine", qemuMachineArg(transport),
		"-cpu", qemuCPUModel(),
		"-display", "none",
		"-serial", "stdio",
		"-no-reboot",
		"-m", strconv.Itoa(bundle.MemoryMiB),
		"-smp", "1",
		"-kernel", bundle.Kernel,
		"-initrd", runtime.RuntimeInitramfs,
		"-append", qemuKernelAppend(),
		"-netdev", netdevArg,
		"-device", qemuNetDeviceArg(transport),
		"-name", name,
	}
	for _, mount := range runtime.Mounts {
		if err := rejectQEMUOptionComma("mount source", mount.Source); err != nil {
			return nil, err
		}
		fsArg := "local,id=" + mount.ID + ",path=" + mount.Source + ",security_model=none"
		if mount.ReadOnly {
			fsArg += ",readonly=on"
		}
		args = append(args,
			"-fsdev", fsArg,
			"-device", qemu9PDeviceArg(transport, mount),
		)
	}
	return args, nil
}

// PCI transport (pcie=on plus virtio-*-pci devices) is the default instead of
// the microvm-native virtio-mmio. A session attaches one virtio-9p device per
// mount/store (~20 devices), which under mmio forces a second IOAPIC and
// leaves the ISA serial with a misrouted IRQ: kernel printk still reaches
// ttyS0 via polled writes, but interrupt-driven userspace console I/O goes
// dead and the payload appears to hang after the last kernel message. PCI +
// MSI-X gives every device its own vectors and keeps COM1 on IRQ 4. mmio
// stays available via ENCLAVE_QEMU_TRANSPORT=mmio for experiments.
func qemuTransport() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ENCLAVE_QEMU_TRANSPORT"))) {
	case "mmio":
		return "mmio"
	default:
		return "pci"
	}
}

func qemuMachineArg(transport string) string {
	if transport == "pci" {
		return "microvm,accel=kvm:tcg,isa-serial=on,pcie=on"
	}
	return "microvm,accel=kvm:tcg,isa-serial=on"
}

func qemuNetDeviceArg(transport string) string {
	if transport == "pci" {
		return "virtio-net-pci,netdev=" + qemuNetdevID
	}
	return "virtio-net-device,netdev=" + qemuNetdevID
}

func qemu9PDeviceArg(transport string, mount runtimeMount) string {
	if transport == "pci" {
		return "virtio-9p-pci,fsdev=" + mount.ID + ",mount_tag=" + mount.Tag
	}
	return "virtio-9p-device,fsdev=" + mount.ID + ",mount_tag=" + mount.Tag
}

func qemuCPUModel() string {
	if value := strings.TrimSpace(os.Getenv("ENCLAVE_QEMU_CPU")); value != "" {
		return value
	}
	return "max"
}

func qemuKernelAppend() string {
	value := "console=ttyS0"
	if extra := strings.TrimSpace(os.Getenv("ENCLAVE_QEMU_KERNEL_APPEND_EXTRA")); extra != "" {
		value += " " + extra
	}
	return value
}

func rejectQEMUOptionComma(label string, value string) error {
	if strings.Contains(value, ",") {
		return fmt.Errorf("qemu backend: %s %q contains a comma, which is not supported in QEMU option values", label, value)
	}
	return nil
}

func (b *Backend) renderRunScript(req backend.Request, mounts []runtimeMount, files []runtimeFileMount) (string, error) {
	if len(req.Argv) == 0 {
		return "", fmt.Errorf("qemu backend: command argv is empty")
	}
	if err := validateUser(req.User); err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("#!/bin/sh\n")
	out.WriteString("set -eu\n")
	out.WriteString("export PATH=/home/" + model.ContainerUser + "/.local/bin:/opt/enclave/node/bin:/sbin:/bin:/usr/sbin:/usr/bin\n")
	out.WriteString("modprobe 9p 2>/dev/null || true\n")
	out.WriteString("modprobe 9pnet 2>/dev/null || true\n")
	out.WriteString("modprobe 9pnet_virtio 2>/dev/null || true\n")
	out.WriteString("modprobe virtio_net 2>/dev/null || true\n")
	out.WriteString("echo " + util.ShellQuote(qemuGuestMessagePrefix+"guest run script starting") + " >&2\n")
	out.WriteString(`
mount_9p() {
  tag="$1"
  target="$2"
  opts="$3"
  echo "` + qemuGuestMessagePrefix + `mounting $tag at $target" >&2
  mkdir -p "$target"
  if ! mount -t 9p -o "trans=virtio,version=9p2000.L,msize=1048576,nosuid,nodev$opts" "$tag" "$target"; then
    echo "` + qemuGuestMessagePrefix + `failed to mount $tag at $target" >&2
    return 1
  fi
}
find_network_interface() {
  for path in /sys/class/net/*; do
    [ -e "$path" ] || continue
    iface=${path##*/}
    [ "$iface" = "lo" ] && continue
    printf '%s\n' "$iface"
    return 0
  done
  return 1
}
setup_network() {
  ip link set dev lo up 2>/dev/null || true
  iface="$(find_network_interface)" || {
    echo "` + qemuGuestMessagePrefix + `missing guest network interface" >&2
    return 1
  }
  ip link set dev "$iface" up
  udhcpc -n -q -i "$iface" >/dev/null
  enclave_network_interface="$iface"
}
enable_route_localnet() {
  iface="$1"
  echo 1 > /proc/sys/net/ipv4/conf/all/route_localnet
  echo 1 > "/proc/sys/net/ipv4/conf/$iface/route_localnet"
}
install_loopback_redirects() {
  if [ "$#" -eq 0 ]; then
    return 0
  fi
  command -v iptables >/dev/null 2>&1 || {
    echo "` + qemuGuestMessagePrefix + `loopback port forwarding requires iptables in the guest" >&2
    return 127
  }
  iface="$enclave_network_interface"
  modprobe ip_tables 2>/dev/null || true
  modprobe iptable_nat 2>/dev/null || true
  modprobe nf_nat 2>/dev/null || true
  modprobe x_tables 2>/dev/null || true
  enable_route_localnet "$iface" || return $?
  for port in "$@"; do
    [ -n "$port" ] || continue
    iptables -t nat -A PREROUTING -i "$iface" -p tcp --dport "$port" -j DNAT --to-destination "127.0.0.1:$port" || return $?
  done
}
install_file_mount() {
  source_path="$1"
  target_path="$2"
  mkdir -p "$(dirname "$target_path")"
  if [ -d "$target_path" ]; then
    echo "` + qemuGuestMessagePrefix + `file mount target $target_path is a directory" >&2
    return 1
  fi
  cp "$source_path" "$target_path"
}
sync_file_mount() {
  target_path="$1"
  result_path="$2"
  mkdir -p "$(dirname "$result_path")"
  if [ -d "$target_path" ]; then
    echo "` + qemuGuestMessagePrefix + `file mount target $target_path is a directory" >&2
    return 1
  fi
  if [ -e "$target_path" ]; then
    cp "$target_path" "$result_path"
  fi
}
install_file_mounts() {
`)
	for _, file := range files {
		fmt.Fprintf(&out, "  install_file_mount %s %s || return $?\n", util.ShellQuote(file.GuestSource), util.ShellQuote(file.Target))
	}
	out.WriteString("  return 0\n}\nsync_file_mounts() {\n")
	for _, file := range files {
		if file.ReadOnly {
			continue
		}
		fmt.Fprintf(&out, "  sync_file_mount %s %s || return $?\n", util.ShellQuote(file.Target), util.ShellQuote(file.GuestResult))
	}
	out.WriteString("  return 0\n}\n")
	if len(mounts) == 0 {
		out.WriteString("echo " + util.ShellQuote(qemuGuestMessagePrefix+"no 9p mounts to attach") + " >&2\n")
	}
	for _, mount := range mounts {
		var opts []string
		if mount.CacheMmap {
			// cache=mmap is needed for SQLite WAL shared-memory files (for
			// example state_*.sqlite-shm) on virtio-9p. Tools opt in per store
			// via qemu_store_cache_mmap in their profile so tools without
			// SQLite do not pay for different 9p cache semantics.
			opts = append(opts, "cache=mmap")
		}
		if mount.ReadOnly {
			opts = append(opts, "ro")
		}
		optSuffix := ""
		if len(opts) > 0 {
			optSuffix = "," + strings.Join(opts, ",")
		}
		fmt.Fprintf(&out, "mount_9p %s %s %s\n", util.ShellQuote(mount.Tag), util.ShellQuote(mount.Target), util.ShellQuote(optSuffix))
	}
	if req.WorkingDir != "" {
		fmt.Fprintf(&out, "mkdir -p %s 2>/dev/null || true\n", util.ShellQuote(req.WorkingDir))
	}
	out.WriteString("enclave_network_interface=\"\"\ncode=0\nsync_code=0\nset +e\n")
	out.WriteString("install_file_mounts\ncode=$?\n")
	out.WriteString("if [ \"$code\" -eq 0 ]; then\n  setup_network\n  code=$?\nfi\n")
	out.WriteString("if [ \"$code\" -eq 0 ]; then\n  install_loopback_redirects")
	for _, port := range req.Network.LoopbackPorts {
		fmt.Fprintf(&out, " %s", util.ShellQuote(port))
	}
	out.WriteString("\n  code=$?\nfi\n")
	out.WriteString("if [ \"$code\" -eq 0 ]; then\n")
	fmt.Fprintf(&out, "  %s\n", b.renderPayloadCommand(req))
	out.WriteString("  code=$?\nfi\n")
	out.WriteString("sync_file_mounts\nsync_code=$?\n")
	out.WriteString("if [ \"$sync_code\" -ne 0 ] && [ \"$code\" -eq 0 ]; then code=$sync_code; fi\n")
	out.WriteString("set -e\n")
	fmt.Fprintf(&out, "printf '%%s\\n' \"$code\" > %s\n", util.ShellQuote(guestExitCodePath))
	out.WriteString("sync || true\npoweroff -f || reboot -f || halt -f || exit \"$code\"\n")
	return out.String(), nil
}

func validateUser(user string) error {
	user = strings.TrimSpace(user)
	switch user {
	case "", "root", model.ContainerUser:
		return nil
	default:
		return fmt.Errorf("qemu backend: only default, root, or %s user execution is supported", model.ContainerUser)
	}
}

func (b *Backend) renderPayloadCommand(req backend.Request) string {
	command := append([]string(nil), req.Argv...)
	if len(req.Entrypoint) > 0 {
		command = append(append([]string(nil), req.Entrypoint...), command...)
	} else {
		command = append([]string{"/usr/local/bin/entrypoint.sh"}, command...)
	}
	if req.WorkingDir != "" {
		command = append([]string{"sh", "-c", `cd "$1" && shift && exec "$@"`, "sh", filepath.Clean(req.WorkingDir)}, command...)
	}
	// setpriv does not synthesize a login environment; provide HOME/USER so
	// tool paths such as ~/.local/bin resolve the same way they did at build time.
	command = append(append([]string{"env"}, qemuPayloadEnv(req)...), command...)
	if req.User == "root" || req.Security.Admin {
		return joinShellQuoted(command)
	}
	uid := strings.TrimSpace(b.opts.Host.UID)
	gid := strings.TrimSpace(b.opts.Host.GID)
	if uid == "" || gid == "" {
		uid = "1000"
		gid = "1000"
	}
	parts := []string{
		// Resolved via the run script's fixed PATH; Alpine installs setpriv
		// under /bin, not /usr/bin.
		"setpriv",
		"--reuid", uid,
		"--regid", gid,
		"--clear-groups",
		"--no-new-privs",
		"--",
	}
	parts = append(parts, command...)
	return joinShellQuoted(parts)
}

func qemuPayloadEnv(req backend.Request) []string {
	env := make([]string, 0, len(req.Env)+2)
	hasHome := false
	hasUser := false
	for _, entry := range req.Env {
		if entry.Name == "" {
			continue
		}
		switch entry.Name {
		case "HOME":
			hasHome = true
		case "USER":
			hasUser = true
		}
		env = append(env, entry.Name+"="+entry.Value)
	}
	user, home := qemuPayloadUserHome(req)
	if !hasHome {
		env = append(env, "HOME="+home)
	}
	if !hasUser {
		env = append(env, "USER="+user)
	}
	return env
}

func qemuPayloadUserHome(req backend.Request) (string, string) {
	if req.User == "root" || req.Security.Admin {
		return "root", "/root"
	}
	return model.ContainerUser, model.ContainerHome
}

func joinShellQuoted(parts []string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, util.ShellQuote(part))
	}
	return strings.Join(quoted, " ")
}
