// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build unix

package docker

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestEnsureOverlayDirIgnoresUmask(t *testing.T) {
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })
	dir := filepath.Join(t.TempDir(), "a2a-overlay", "session")
	if err := ensureOverlayDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("overlay dir mode = %v, %v", info, err)
	}
}
