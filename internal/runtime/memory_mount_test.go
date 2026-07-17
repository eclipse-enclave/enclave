// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

func TestAddMemoryMountsSkipsWhenMemoryDirUnset(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		host:          model.Host{Home: t.TempDir()},
		project:       model.Project{Hash: "projhash"},
		profile:       model.Profile{Name: "codex"},
		containerHome: "/home/agent",
	}
	acc := newMountAccumulator(nil, nil)
	r.addMemoryMounts(acc)
	if len(acc.Mounts()) != 0 {
		t.Fatalf("expected no mounts, got %d", len(acc.Mounts()))
	}
}

func TestAddMemoryMountsBindsDirectory(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	projectHash := "projhash"
	r := &Runtime{
		host:          model.Host{Home: home},
		project:       model.Project{Hash: projectHash},
		profile:       model.Profile{Name: "claude", MemoryDir: ".claude/memory"},
		containerHome: "/home/agent",
	}

	acc := newMountAccumulator(nil, nil)
	r.addMemoryMounts(acc)

	target := "/home/agent/.claude/memory"
	source, ok := lookupMountSource(acc.Mounts(), target)
	if !ok {
		t.Fatalf("expected memory mount at %s", target)
	}
	if want := config.HostProjectMemoryDir(home, projectHash, "claude"); source != want {
		t.Fatalf("memory mount source = %q, want %q", source, want)
	}
	for _, m := range acc.Mounts() {
		if m.ContainerPath == target && m.ReadOnly {
			t.Fatalf("expected memory mount to be writable")
		}
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("expected host memory directory to exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %s to be a directory", source)
	}
}

func TestAddMemoryMountsBindsFiles(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	projectHash := "projhash"
	r := &Runtime{
		host:          model.Host{Home: home},
		project:       model.Project{Hash: projectHash},
		profile:       model.Profile{Name: "gemini", MemoryFiles: []string{".gemini/GEMINI.md"}},
		containerHome: "/home/agent",
	}

	acc := newMountAccumulator(nil, nil)
	r.addMemoryMounts(acc)

	target := "/home/agent/.gemini/GEMINI.md"
	source, ok := lookupMountSource(acc.Mounts(), target)
	if !ok {
		t.Fatalf("expected memory file mount at %s", target)
	}
	want := filepath.Join(config.HostProjectMemoryDir(home, projectHash, "gemini"), "GEMINI.md")
	if source != want {
		t.Fatalf("memory file mount source = %q, want %q", source, want)
	}
	for _, m := range acc.Mounts() {
		if m.ContainerPath == target && m.ReadOnly {
			t.Fatalf("expected memory file mount to be writable")
		}
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("expected host memory file to exist: %v", err)
	}
	if info.IsDir() {
		t.Fatalf("expected %s to be a file, not a directory", source)
	}
}

func TestAddMemoryMountsRespectsNoMemory(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		host:          model.Host{Home: t.TempDir()},
		project:       model.Project{Hash: "projhash"},
		profile:       model.Profile{Name: "claude", MemoryDir: ".claude/memory"},
		containerHome: "/home/agent",
		run:           model.RunOptions{NoMemory: true},
	}

	acc := newMountAccumulator(nil, nil)
	r.addMemoryMounts(acc)
	if len(acc.Mounts()) != 0 {
		t.Fatalf("expected no mounts when NoMemory is set, got %d", len(acc.Mounts()))
	}
}

func TestAddMemoryMountsRespectsEphemeral(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		host:          model.Host{Home: t.TempDir()},
		project:       model.Project{Hash: "projhash"},
		profile:       model.Profile{Name: "claude", MemoryDir: ".claude/memory", MemoryFiles: []string{".gemini/GEMINI.md"}},
		containerHome: "/home/agent",
		run:           model.RunOptions{Ephemeral: true},
	}

	acc := newMountAccumulator(nil, nil)
	r.addMemoryMounts(acc)
	if len(acc.Mounts()) != 0 {
		t.Fatalf("expected no mounts for ephemeral session, got %d", len(acc.Mounts()))
	}
}
