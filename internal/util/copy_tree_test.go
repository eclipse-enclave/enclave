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
	"testing"
)

func TestCopyTreeCopiesNestedFilesUsingCallback(t *testing.T) {
	srcRoot := filepath.Join(t.TempDir(), "src")
	dstRoot := filepath.Join(t.TempDir(), "dst")

	writeTreeFile(t, filepath.Join(srcRoot, "top.txt"), "top", 0o640)
	writeTreeFile(t, filepath.Join(srcRoot, "nested", "leaf.txt"), "leaf", 0o600)

	if err := CopyTree(srcRoot, dstRoot, func(src string, dst string, mode os.FileMode) error {
		// #nosec G304 -- src is controlled by test setup under t.TempDir.
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, mode)
	}); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}

	assertTreeFile(t, filepath.Join(dstRoot, "top.txt"), "top", 0o640)
	assertTreeFile(t, filepath.Join(dstRoot, "nested", "leaf.txt"), "leaf", 0o600)
}

func TestCopyTreeRequiresCallback(t *testing.T) {
	srcRoot := filepath.Join(t.TempDir(), "src")
	writeTreeFile(t, filepath.Join(srcRoot, "file.txt"), "value", 0o644)

	if err := CopyTree(srcRoot, filepath.Join(t.TempDir(), "dst"), nil); err == nil {
		t.Fatal("expected error for nil copy callback")
	}
}

func writeTreeFile(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertTreeFile(t *testing.T, path string, wantContent string, wantPerm os.FileMode) {
	t.Helper()
	// #nosec G304 -- path is controlled by test setup under t.TempDir.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := string(data); got != wantContent {
		t.Fatalf("content mismatch for %s: got %q want %q", path, got, wantContent)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != wantPerm {
		t.Fatalf("mode mismatch for %s: got %#o want %#o", path, got, wantPerm)
	}
}
