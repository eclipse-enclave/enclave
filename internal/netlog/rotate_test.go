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
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the live log survived rotation; it should be recreated by the writer")
	}
	info, err := os.Stat(RotatedPath(path))
	if err != nil {
		t.Fatalf("stat rotated log: %v", err)
	}
	if info.Size() != 2048 {
		t.Fatalf("rotated log size = %d, want 2048", info.Size())
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
