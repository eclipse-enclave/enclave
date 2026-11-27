// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

func TestAddSSHMountUsesConfigDirectoryReadOnly(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	sshDir := config.HostSSHDir(home)
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("create SSH directory: %v", err)
	}

	r := &Runtime{
		host:          model.Host{Home: home},
		containerHome: "/home/enclave",
	}
	mounts := newMountAccumulator(nil, nil)
	r.addSSHMount(mounts)

	got := mounts.Mounts()
	if len(got) != 1 {
		t.Fatalf("SSH mounts = %d, want 1", len(got))
	}
	mount := got[0]
	if mount.Type != backend.MountTypeBind {
		t.Fatalf("mount type = %q, want bind", mount.Type)
	}
	if mount.Source != sshDir {
		t.Fatalf("mount source = %q, want %q", mount.Source, sshDir)
	}
	if mount.ContainerPath != "/home/enclave/.ssh" {
		t.Fatalf("mount destination = %q, want /home/enclave/.ssh", mount.ContainerPath)
	}
	if !mount.ReadOnly {
		t.Fatal("SSH mount is writable, want read-only")
	}
}

func TestAddSSHMountDoesNotFallBackToLegacyDirectory(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := os.MkdirAll(config.HostLegacySSHDir(home), 0o700); err != nil {
		t.Fatalf("create legacy SSH directory: %v", err)
	}

	r := &Runtime{
		host:          model.Host{Home: home},
		containerHome: "/home/enclave",
	}
	mounts := newMountAccumulator(nil, nil)
	r.addSSHMount(mounts)

	if got := len(mounts.Mounts()); got != 0 {
		t.Fatalf("legacy SSH mounts = %d, want 0", got)
	}
}
