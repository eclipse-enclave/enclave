// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// readOnlyDir returns a temporary directory the test process cannot create
// files in. Root bypasses permission bits, so the test is skipped as root.
func readOnlyDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not restrict writes on Windows")
	}
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir
}

// The read-only lookup must choose the same home as the write probe, or
// discovery and completion read a different home than the commands that write.
func TestLooksWritableDirAgreesWithWriteProbe(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  string
		want bool
	}{
		{"writable", t.TempDir(), true},
		{"read-only", readOnlyDir(t), false},
		{"missing", filepath.Join(t.TempDir(), "missing"), false},
	} {
		if got := IsWritableDir(tc.dir); got != tc.want {
			t.Errorf("%s: IsWritableDir = %v, want %v", tc.name, got, tc.want)
		}
		if got := looksWritableDir(tc.dir); got != tc.want {
			t.Errorf("%s: looksWritableDir = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestResolveHostHomeReadOnlyPassesOverNonWritableHome(t *testing.T) {
	home := readOnlyDir(t)
	t.Setenv("HOME", home)

	// The user database home is next; an error only means it is not writable
	// either.
	if got, err := ResolveHostHomeReadOnly(); err == nil && got == home {
		t.Fatalf("ResolveHostHomeReadOnly chose the non-writable HOME %q", home)
	}
}

func TestResolveHostHomeReadOnlySkipsWriteProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME does not select the home directory on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Creating and removing a probe file would bump the directory's mtime.
	pinned := time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(home, pinned, pinned); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveHostHomeReadOnly()
	if err != nil {
		t.Fatalf("ResolveHostHomeReadOnly: %v", err)
	}
	if got != home {
		t.Fatalf("ResolveHostHomeReadOnly = %q, want %q", got, home)
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(pinned) {
		t.Errorf("HOME was written to: mtime %s", info.ModTime())
	}
}
