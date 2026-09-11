// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSized(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRotateIfLargerBelowCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	writeSized(t, path, 100)

	rotated, err := RotateIfLarger(path, 1024)
	if err != nil {
		t.Fatalf("RotateIfLarger() error = %v", err)
	}
	if rotated {
		t.Fatal("rotated a log below the cap")
	}
	if _, err := os.Stat(RotatedPath(path)); !os.IsNotExist(err) {
		t.Fatal("a previous generation was created below the cap")
	}
}

func TestRotateIfLargerAboveCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	writeSized(t, path, 2048)

	rotated, err := RotateIfLarger(path, 1024)
	if err != nil {
		t.Fatalf("RotateIfLarger() error = %v", err)
	}
	if !rotated {
		t.Fatal("did not rotate a log above the cap")
	}
	live, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat live log: %v", err)
	}
	if live.Size() != 0 {
		t.Fatalf("live log size = %d, want it truncated in place", live.Size())
	}
	info, err := os.Stat(RotatedPath(path))
	if err != nil {
		t.Fatalf("stat rotated log: %v", err)
	}
	if info.Size() != 2048 {
		t.Fatalf("rotated log size = %d, want 2048", info.Size())
	}
}

func TestRotateIfLargerKeepsTheInodeOpenWritersHold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	writeSized(t, path, 2048)

	// A gateway of an already running session bind-mounts this file, so a
	// rotation that swapped the inode would leave it appending to the rotated
	// generation, out of reach of every reader.
	writer, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer func() { _ = writer.Close() }()

	if _, err := RotateIfLarger(path, 1024); err != nil {
		t.Fatalf("RotateIfLarger() error = %v", err)
	}
	if _, err := writer.WriteString("after\n"); err != nil {
		t.Fatalf("append after rotation: %v", err)
	}

	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read live log: %v", err)
	}
	if string(live) != "after\n" {
		t.Fatalf("live log = %q, want the post-rotation append", string(live))
	}
}

func TestRotateIfLargerReplacesExistingGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.log")
	writeSized(t, path, 2048)
	writeSized(t, RotatedPath(path), 7)

	if _, err := RotateIfLarger(path, 1024); err != nil {
		t.Fatalf("RotateIfLarger() error = %v", err)
	}
	info, err := os.Stat(RotatedPath(path))
	if err != nil {
		t.Fatalf("stat rotated log: %v", err)
	}
	if info.Size() != 2048 {
		t.Fatalf("rotated log size = %d, want the newer generation to replace the older", info.Size())
	}
}

// Two sessions of one project and tool can start at once. The second must not
// replace the generation the first just wrote with the file the first emptied.
func TestRotateIfLargerConcurrentRotationsKeepTheGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	writeSized(t, path, 4096)

	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			_, err := RotateIfLarger(path, 1024)
			results <- err
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-results; err != nil {
			t.Fatalf("RotateIfLarger() error = %v", err)
		}
	}

	info, err := os.Stat(RotatedPath(path))
	if err != nil {
		t.Fatalf("stat rotated log: %v", err)
	}
	if info.Size() != 4096 {
		t.Fatalf("rotated log size = %d, want the full 4096 bytes of history", info.Size())
	}
}

func TestRotateIfLargerDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	writeSized(t, path, 4096)

	for _, cap := range []int64{0, -1} {
		rotated, err := RotateIfLarger(path, cap)
		if err != nil {
			t.Fatalf("RotateIfLarger(%d) error = %v", cap, err)
		}
		if rotated {
			t.Fatalf("rotated with the cap disabled (%d)", cap)
		}
	}
}

func TestRotateIfLargerMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	rotated, err := RotateIfLarger(path, 1024)
	if err != nil || rotated {
		t.Fatalf("RotateIfLarger() = %v, %v; want false, nil", rotated, err)
	}
}

func TestReadPathsPrefersTheRotatedGenerationFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.log")

	if paths := ReadPaths(path); len(paths) != 0 {
		t.Fatalf("ReadPaths() = %v, want none", paths)
	}

	writeSized(t, path, 1)
	if paths := ReadPaths(path); len(paths) != 1 || paths[0] != path {
		t.Fatalf("ReadPaths() = %v, want [%s]", paths, path)
	}

	writeSized(t, RotatedPath(path), 1)
	paths := ReadPaths(path)
	if len(paths) != 2 || paths[0] != RotatedPath(path) || paths[1] != path {
		t.Fatalf("ReadPaths() = %v, want the rotated log first", paths)
	}
}
