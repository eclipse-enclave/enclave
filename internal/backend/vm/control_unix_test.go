// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package vm

import (
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadGuestFileDoesNotBlockOnAFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(dir, "boot"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := readGuestFile(dir, "boot", 16)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO was read as a guest status file")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO the guest planted blocked the host")
	}
}
