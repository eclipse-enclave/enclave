// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package docker

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestForeignOwnedStorePathReportsOutermostMismatch(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "shared", "skills")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	me := strconv.Itoa(os.Getuid())
	other := strconv.Itoa(os.Getuid() + 1)

	if got := foreignOwnedStorePath(root, target, me); got != "" {
		t.Fatalf("foreignOwnedStorePath() = %q, want no mismatch for the invoking user", got)
	}
	// Every component is owned by the current user, so a foreign uid must flag
	// the outermost one below the store root rather than the leaf.
	if got, want := foreignOwnedStorePath(root, target, other), filepath.Join(root, "shared"); got != want {
		t.Fatalf("foreignOwnedStorePath() = %q, want %q", got, want)
	}
	if got := foreignOwnedStorePath(root, root, other); got != "" {
		t.Fatalf("foreignOwnedStorePath(root, root) = %q, want empty", got)
	}
}
