// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package hoststore

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRootedReadsRejectFIFOWithoutOpeningItBlocking(t *testing.T) {
	dir := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := root.ReadFile("fifo"); err == nil {
		t.Fatal("FIFO accepted as regular file")
	}
	if _, err := root.ReadDir("fifo"); err == nil {
		t.Fatal("FIFO accepted as directory")
	}
}
