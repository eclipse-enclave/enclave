// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package qemu

import (
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestRenderPayloadCommandSetsAgentHomeAndUser(t *testing.T) {
	be := New(Options{Host: model.Host{UID: "1234", GID: "5678"}})

	cmd := be.renderPayloadCommand(backend.Request{Argv: []string{"bash", "-lc", "echo ok"}})

	assertContains(t, cmd, "'setpriv' '--reuid' '1234' '--regid' '5678'")
	assertContains(t, cmd, "'env' 'HOME=/home/agent' 'USER=agent'")
	assertContains(t, cmd, "'/usr/local/bin/entrypoint.sh'")
}

func TestRenderRunScriptMountsWithMmapCacheWhenRequested(t *testing.T) {
	be := New(Options{})

	script, err := be.renderRunScript(backend.Request{Argv: []string{"true"}}, []runtimeMount{{Tag: "tag-0", Target: "/home/agent/.codex", CacheMmap: true}}, nil)
	if err != nil {
		t.Fatalf("renderRunScript: %v", err)
	}

	assertContains(t, script, "mount_9p 'tag-0' '/home/agent/.codex' ',cache=mmap'")
}

func TestRenderRunScriptDoesNotMountWithMmapCacheByDefault(t *testing.T) {
	be := New(Options{})

	script, err := be.renderRunScript(backend.Request{Argv: []string{"true"}}, []runtimeMount{{Tag: "tag-0", Target: "/home/agent/.claude"}}, nil)
	if err != nil {
		t.Fatalf("renderRunScript: %v", err)
	}

	assertNotContains(t, script, "cache=mmap")
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
	assertContainsArg(t, args, "virtio-net-pci,netdev="+qemuNetdevID)
	assertContainsArg(t, args, "virtio-9p-pci,fsdev=mount-0,mount_tag=tag-0")
	assertNotContainsArg(t, args, "virtio-net-device,netdev="+qemuNetdevID)
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
