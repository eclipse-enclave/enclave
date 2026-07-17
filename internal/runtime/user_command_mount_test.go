// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

// TestAddUserCommandMountReadOnlyAtFixedPath verifies that a session user
// command mounts the host session command tree read-only at the fixed neutral
// container path, independent of the host home layout.
func TestAddUserCommandMountReadOnlyAtFixedPath(t *testing.T) {
	t.Parallel()

	home := "/home/alice"
	sessionDir := config.HostCommandsSessionDir(home)
	r := &Runtime{
		userCommandMount: &model.UserCommandMount{
			HostDir:       sessionDir,
			ContainerPath: model.UserCommandsContainerDir,
		},
	}

	acc := newMountAccumulator(nil, nil)
	r.addUserCommandMount(acc)

	mnts := acc.Mounts()
	if len(mnts) != 1 {
		t.Fatalf("expected exactly one mount, got %d: %+v", len(mnts), mnts)
	}
	m := mnts[0]
	if m.Type != backend.MountTypeBind {
		t.Fatalf("expected bind mount, got %q", m.Type)
	}
	if m.Source != sessionDir {
		t.Fatalf("expected source %q, got %q", sessionDir, m.Source)
	}
	if m.ContainerPath != model.UserCommandsContainerDir {
		t.Fatalf("expected container path %q, got %q", model.UserCommandsContainerDir, m.ContainerPath)
	}
	if !m.ReadOnly {
		t.Fatalf("expected user command mount to be read-only")
	}
}

// TestAddUserCommandMountNilIsNoop verifies that runs without a session user
// command add no mount.
func TestAddUserCommandMountNilIsNoop(t *testing.T) {
	t.Parallel()

	r := &Runtime{}
	acc := newMountAccumulator(nil, nil)
	r.addUserCommandMount(acc)

	if got := len(acc.Mounts()); got != 0 {
		t.Fatalf("expected no mounts for nil userCommandMount, got %d", got)
	}
}

// TestAddUserCommandMountNeverReferencesHostTree is the isolation-boundary
// guard: even when a session command is mounted, no produced mount may
// reference the host/ command tree (which must stay invisible in-container).
func TestAddUserCommandMountNeverReferencesHostTree(t *testing.T) {
	t.Parallel()

	home := "/home/bob"
	hostDir := config.HostCommandsHostDir(home)
	r := &Runtime{
		userCommandMount: &model.UserCommandMount{
			HostDir:       config.HostCommandsSessionDir(home),
			ContainerPath: model.UserCommandsContainerDir,
		},
	}

	acc := newMountAccumulator(nil, nil)
	r.addUserCommandMount(acc)

	for _, m := range acc.Mounts() {
		if strings.HasPrefix(m.Source, hostDir) {
			t.Fatalf("host command tree %q must never be mounted, got source %q", hostDir, m.Source)
		}
	}
}
