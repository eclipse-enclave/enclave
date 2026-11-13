// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package util

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestReadRegularFileRejectsFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	for _, read := range []func(string, int64) ([]byte, error){ReadRegularFile, ReadRegularFileNoFollow} {
		if _, err := read(fifo, 5); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("read of a FIFO = %v", err)
		}
	}
}
