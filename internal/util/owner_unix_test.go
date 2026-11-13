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
	"strconv"
	"testing"
)

type infoWithoutStat struct{ os.FileInfo }

func (infoWithoutStat) Sys() any { return nil }

func TestFileInfoOwnedBy(t *testing.T) {
	t.Parallel()

	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	me := strconv.Itoa(os.Getuid())
	other := strconv.Itoa(os.Getuid() + 1)

	if !FileInfoOwnedBy(info, me) {
		t.Fatalf("FileInfoOwnedBy(%s) = false, want true for the invoking user", me)
	}
	if FileInfoOwnedBy(info, other) {
		t.Fatalf("FileInfoOwnedBy(%s) = true, want false for a different uid", other)
	}
	if !FileInfoOwnedBy(info, "not-a-uid") {
		t.Fatal("FileInfoOwnedBy with an unparseable uid should report true (undetermined)")
	}
	if !FileInfoOwnedBy(infoWithoutStat{info}, other) {
		t.Fatal("FileInfoOwnedBy without POSIX ownership should report true (undetermined)")
	}
}
