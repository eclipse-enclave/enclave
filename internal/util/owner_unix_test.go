// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package util

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestPathOwnedBy(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	me := strconv.Itoa(os.Getuid())
	other := strconv.Itoa(os.Getuid() + 1)

	if !PathOwnedBy(dir, me) {
		t.Fatalf("PathOwnedBy(%q, %s) = false, want true for the invoking user", dir, me)
	}
	if PathOwnedBy(dir, other) {
		t.Fatalf("PathOwnedBy(%q, %s) = true, want false for a different uid", dir, other)
	}
	if !PathOwnedBy(filepath.Join(dir, "missing"), other) {
		t.Fatal("PathOwnedBy on a missing path should report true (undetermined)")
	}
	if !PathOwnedBy(dir, "not-a-uid") {
		t.Fatal("PathOwnedBy with an unparseable uid should report true (undetermined)")
	}
}
