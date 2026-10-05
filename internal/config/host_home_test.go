// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"runtime"
	"testing"
	"time"
)

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
