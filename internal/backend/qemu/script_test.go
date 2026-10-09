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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestReadOnlyFileMountDoesNotOverwriteHostFile(t *testing.T) {
	be := New(Options{})
	root := t.TempDir()
	source, target := filepath.Join(root, "staged"), filepath.Join(root, "host-config")
	for path, content := range map[string]string{source: "snapshot", target: "host-original"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script, err := be.renderRunScript(backend.Request{Argv: []string{"true"}}, nil, []runtimeFileMount{{GuestSource: source, Target: target, ReadOnly: true}}, defaultConsoleSize)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, script, "install_readonly_file_mount '"+source+"' '"+target+"'")
	assertNotContains(t, script, "install_file_mount '"+source+"' '"+target+"'")
	if out, err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").CombinedOutput(); err != nil {
		t.Skipf("mount namespaces unavailable: %v: %s", err, out)
	}
	start := strings.Index(script, "install_readonly_file_mount() {")
	end := strings.Index(script[start:], "sync_file_mount() {")
	check := script[start:start+end] + `
install_readonly_file_mount "$1" "$2"
test "$(cat "$2")" = snapshot
if printf 'changed' > "$2"; then exit 1; fi
if rm "$2"; then exit 1; fi
`
	if out, err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "sh", "-eu", "-c", check, "qemu-file-test", source, target).CombinedOutput(); err != nil {
		t.Fatalf("read-only file mount: %v\n%s", err, out)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "host-original" {
		t.Fatalf("host file changed: %q, %v", data, err)
	}
}

func TestRenderPayloadCommandSetsAgentHomeAndUser(t *testing.T) {
	be := New(Options{Host: model.Host{UID: "1234", GID: "5678"}})

	cmd := be.renderPayloadCommand(backend.Request{Argv: []string{"bash", "-lc", "echo ok"}})

	assertContains(t, cmd, "'setpriv' '--reuid' '1234' '--regid' '5678'")
	assertContains(t, cmd, "'env' 'HOME=/home/agent' 'USER=agent'")
	assertContains(t, cmd, "'/usr/local/bin/entrypoint.sh'")
}

func TestRenderRunScriptMountsWithMmapCacheWhenRequested(t *testing.T) {
	be := New(Options{})

	script, err := be.renderRunScript(backend.Request{Argv: []string{"true"}}, []runtimeMount{{Tag: "tag-0", Target: "/home/agent/.codex", CacheMmap: true}}, nil, defaultConsoleSize)
	if err != nil {
		t.Fatalf("renderRunScript: %v", err)
	}

	assertContains(t, script, "mount_9p 'tag-0' '/home/agent/.codex' ',cache=mmap'")
}

func TestRenderRunScriptDoesNotMountWithMmapCacheByDefault(t *testing.T) {
	be := New(Options{})

	script, err := be.renderRunScript(backend.Request{Argv: []string{"true"}}, []runtimeMount{{Tag: "tag-0", Target: "/home/agent/.claude"}}, nil, defaultConsoleSize)
	if err != nil {
		t.Fatalf("renderRunScript: %v", err)
	}

	assertNotContains(t, script, "cache=mmap")
}

// The serial console starts out 0x0, which leaves terminal UIs unrenderable.
func TestRenderRunScriptSeedsConsoleSize(t *testing.T) {
	be := New(Options{})

	script, err := be.renderRunScript(backend.Request{Argv: []string{"true"}}, nil, nil, consoleSize{Rows: 47, Cols: 173})
	if err != nil {
		t.Fatalf("renderRunScript: %v", err)
	}

	assertContains(t, script, "stty rows 47 cols 173")
}

func TestResolveConsoleSizeFallsBackToDefault(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = read.Close()
		_ = write.Close()
	}()

	got := resolveConsoleSize(backend.AttachIO{In: read, Out: write, Err: write})
	if got != defaultConsoleSize {
		t.Fatalf("consoleSize = %+v, want %+v", got, defaultConsoleSize)
	}
}

func TestRenderPayloadCommandPreservesExplicitHomeAndUser(t *testing.T) {
	be := New(Options{Host: model.Host{UID: "1234", GID: "5678"}})

	cmd := be.renderPayloadCommand(backend.Request{
		Argv: []string{"true"},
		Env: []backend.EnvVar{
			{Name: "HOME", Value: "/custom-home"},
			{Name: "USER", Value: "custom-user"},
			{Name: "FOO", Value: "bar"},
		},
	})

	assertContains(t, cmd, "'env' 'HOME=/custom-home' 'USER=custom-user' 'FOO=bar'")
	assertNotContains(t, cmd, "HOME=/home/agent")
	assertNotContains(t, cmd, "USER=agent")
}

func TestRenderPayloadCommandSetsRootHomeAndUserForAdmin(t *testing.T) {
	be := New(Options{Host: model.Host{UID: "1234", GID: "5678"}})

	cmd := be.renderPayloadCommand(backend.Request{
		Argv:     []string{"id"},
		Security: backend.SecurityPosture{Admin: true},
	})

	assertNotContains(t, cmd, "setpriv")
	assertContains(t, cmd, "'env' 'HOME=/root' 'USER=root'")
}

func TestBuildQEMUArgsUsesPCITransportByDefault(t *testing.T) {
	be := New(Options{})

	args, err := be.buildQEMUArgs(bundle{Kernel: "/kernel", Initramfs: "/initramfs", MemoryMiB: 512}, guestRuntime{
		RuntimeInitramfs: "/runtime-initramfs",
		Mounts:           []runtimeMount{{ID: "mount-0", Tag: "tag-0", Source: "/tmp"}},
	}, backend.Request{})
	if err != nil {
		t.Fatalf("buildQEMUArgs: %v", err)
	}

	assertArgValue(t, args, "-machine", "microvm,accel=kvm:tcg,isa-serial=on,pcie=on")
	assertContainsArg(t, args, "virtio-net-pci,netdev="+qemuNetdevID+",addr=0x1.0x0,multifunction=on")
	assertContainsArg(t, args, "virtio-9p-pci,fsdev=mount-0,mount_tag=tag-0,addr=0x1.0x1")
	assertNotContainsArg(t, args, "virtio-net-device,netdev="+qemuNetdevID)
}

func TestBuildQEMUArgsPacksPCIDevicesIntoMultifunctionSlots(t *testing.T) {
	be := New(Options{})
	var mounts []runtimeMount
	for i := range 40 {
		mounts = append(mounts, runtimeMount{ID: fmt.Sprintf("mount-%d", i), Tag: fmt.Sprintf("tag-%d", i), Source: "/tmp"})
	}

	args, err := be.buildQEMUArgs(bundle{Kernel: "/kernel", Initramfs: "/initramfs", MemoryMiB: 512}, guestRuntime{
		RuntimeInitramfs: "/runtime-initramfs",
		Mounts:           mounts,
	}, backend.Request{})
	if err != nil {
		t.Fatalf("buildQEMUArgs: %v", err)
	}

	assertContainsArg(t, args, "virtio-9p-pci,fsdev=mount-6,mount_tag=tag-6,addr=0x1.0x7")
	assertContainsArg(t, args, "virtio-9p-pci,fsdev=mount-7,mount_tag=tag-7,addr=0x2.0x0,multifunction=on")
	assertContainsArg(t, args, "virtio-9p-pci,fsdev=mount-38,mount_tag=tag-38,addr=0x5.0x7")
	assertContainsArg(t, args, "virtio-9p-pci,fsdev=mount-39,mount_tag=tag-39,addr=0x6.0x0")
}

func TestBuildQEMUArgsRejectsMorePCIDevicesThanTheRootBusHolds(t *testing.T) {
	be := New(Options{})
	mounts := make([]runtimeMount, 248)
	for i := range mounts {
		mounts[i] = runtimeMount{ID: fmt.Sprintf("mount-%d", i), Tag: fmt.Sprintf("tag-%d", i), Source: "/tmp"}
	}

	_, err := be.buildQEMUArgs(bundle{Kernel: "/kernel", Initramfs: "/initramfs", MemoryMiB: 512}, guestRuntime{
		RuntimeInitramfs: "/runtime-initramfs",
		Mounts:           mounts,
	}, backend.Request{})
	if err == nil || !strings.Contains(err.Error(), "249 devices exceed the 248 PCI functions") {
		t.Fatalf("buildQEMUArgs error = %v, want PCI capacity error", err)
	}
}

func TestBuildQEMUArgsCanUseMMIOTransport(t *testing.T) {
	t.Setenv("ENCLAVE_QEMU_TRANSPORT", "mmio")
	be := New(Options{})

	args, err := be.buildQEMUArgs(bundle{Kernel: "/kernel", Initramfs: "/initramfs", MemoryMiB: 512}, guestRuntime{
		RuntimeInitramfs: "/runtime-initramfs",
		Mounts:           []runtimeMount{{ID: "mount-0", Tag: "tag-0", Source: "/tmp"}},
	}, backend.Request{})
	if err != nil {
		t.Fatalf("buildQEMUArgs: %v", err)
	}

	assertArgValue(t, args, "-machine", "microvm,accel=kvm:tcg,isa-serial=on")
	assertContainsArg(t, args, "virtio-net-device,netdev="+qemuNetdevID)
	assertContainsArg(t, args, "virtio-9p-device,fsdev=mount-0,mount_tag=tag-0")
	assertNotContainsArg(t, args, "virtio-net-pci,netdev="+qemuNetdevID)
}

func TestBuildQEMUArgsUsesDebugOverrides(t *testing.T) {
	t.Setenv("ENCLAVE_QEMU_CPU", "qemu64")
	t.Setenv("ENCLAVE_QEMU_KERNEL_APPEND_EXTRA", "ignore_loglevel initcall_debug")
	be := New(Options{})

	args, err := be.buildQEMUArgs(bundle{Kernel: "/kernel", Initramfs: "/initramfs", MemoryMiB: 512}, guestRuntime{RuntimeInitramfs: "/runtime-initramfs"}, backend.Request{})
	if err != nil {
		t.Fatalf("buildQEMUArgs: %v", err)
	}

	assertArgValue(t, args, "-cpu", "qemu64")
	assertArgValue(t, args, "-append", "console=ttyS0 ignore_loglevel initcall_debug")
}

func TestBuildQEMUArgsRejectsCommaInMountSource(t *testing.T) {
	be := New(Options{})

	_, err := be.buildQEMUArgs(bundle{Kernel: "/kernel", Initramfs: "/initramfs", MemoryMiB: 512}, guestRuntime{
		RuntimeInitramfs: "/runtime-initramfs",
		Mounts: []runtimeMount{{
			ID:     "mount-0",
			Tag:    "tag-0",
			Source: "/tmp/with,comma",
			Target: "/workspace",
		}},
	}, backend.Request{})
	if err == nil || !strings.Contains(err.Error(), "contains a comma") {
		t.Fatalf("expected comma validation error, got %v", err)
	}
}

func TestBuildQEMUArgsRejectsCommaInSessionName(t *testing.T) {
	be := New(Options{})

	_, err := be.buildQEMUArgs(bundle{Kernel: "/kernel", Initramfs: "/initramfs", MemoryMiB: 512}, guestRuntime{RuntimeInitramfs: "/runtime-initramfs"}, backend.Request{
		Session: backend.SessionMeta{Name: "session,with-comma"},
	})
	if err == nil || !strings.Contains(err.Error(), "contains a comma") {
		t.Fatalf("expected comma validation error, got %v", err)
	}
}

func TestBuildQEMUArgsRejectsCommaInHostForward(t *testing.T) {
	be := New(Options{})

	_, err := be.buildQEMUArgs(bundle{Kernel: "/kernel", Initramfs: "/initramfs", MemoryMiB: 512}, guestRuntime{RuntimeInitramfs: "/runtime-initramfs"}, backend.Request{
		Ports: []backend.PortMapping{{HostIP: "127.0.0.1,evil", HostPort: "3000", ContainerPort: "3000", Protocol: "tcp"}},
	})
	if err == nil || !strings.Contains(err.Error(), "contains a comma") {
		t.Fatalf("expected comma validation error, got %v", err)
	}
}

func assertContainsArg(t *testing.T, args []string, want string) {
	t.Helper()
	for _, arg := range args {
		if arg == want {
			return
		}
	}
	t.Fatalf("arg %q not found in %#v", want, args)
}

func assertNotContainsArg(t *testing.T, args []string, unwanted string) {
	t.Helper()
	for _, arg := range args {
		if arg == unwanted {
			t.Fatalf("arg %q unexpectedly found in %#v", unwanted, args)
		}
	}
}

func assertArgValue(t *testing.T, args []string, key string, want string) {
	t.Helper()
	for i, arg := range args {
		if arg == key && i+1 < len(args) {
			if args[i+1] != want {
				t.Fatalf("%s = %q, want %q", key, args[i+1], want)
			}
			return
		}
	}
	t.Fatalf("arg %s not found in %#v", key, args)
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected %q to contain %q", haystack, needle)
	}
}

func assertNotContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("expected %q not to contain %q", haystack, needle)
	}
}
