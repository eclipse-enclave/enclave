// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRegularFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Skipf("symlink: %v", err)
	}

	for _, read := range []func(string, int64) ([]byte, error){ReadRegularFile, ReadRegularFileNoFollow} {
		if data, err := read(file, 5); err != nil || string(data) != "12345" {
			t.Fatalf("read at the limit = %q, %v", data, err)
		}
		if _, err := read(file, 4); err == nil || !strings.Contains(err.Error(), "larger than 4 bytes") {
			t.Fatalf("read over the limit = %v", err)
		}
		if _, err := read(dir, 5); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("read of a directory = %v", err)
		}
		if _, err := read(filepath.Join(dir, "missing"), 5); !os.IsNotExist(err) {
			t.Fatalf("read of a missing file = %v", err)
		}
	}
	if data, err := ReadRegularFile(link, 5); err != nil || string(data) != "12345" {
		t.Fatalf("ReadRegularFile(symlink) = %q, %v", data, err)
	}
	if _, err := ReadRegularFileNoFollow(link, 5); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("ReadRegularFileNoFollow(symlink) = %v", err)
	}
}
