// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyExtensionTreeCopiesContentAndNormalizesModes(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	writeFixture(t, filepath.Join(src, "spec.yaml"), "schemaVersion: \"1\"\n", 0o600)
	writeFixture(t, filepath.Join(src, "install.sh"), "#!/bin/sh\n", 0o700)
	writeFixture(t, filepath.Join(src, "templates", "settings.json"), "{}\n", 0o644)

	if err := copyExtensionTree(src, dst, defaultLimits()); err != nil {
		t.Fatalf("copyExtensionTree: %v", err)
	}
	if got := countFiles(t, dst); got != 3 {
		t.Errorf("copied %d files, want 3", got)
	}

	spec, err := os.Stat(filepath.Join(dst, "spec.yaml"))
	if err != nil {
		t.Fatalf("stat spec.yaml: %v", err)
	}
	if spec.Mode().Perm()&0o111 != 0 {
		t.Errorf("spec.yaml mode = %v, want no execute bit", spec.Mode().Perm())
	}
	script, err := os.Stat(filepath.Join(dst, "install.sh"))
	if err != nil {
		t.Fatalf("stat install.sh: %v", err)
	}
	if script.Mode().Perm()&0o100 == 0 {
		t.Errorf("install.sh mode = %v, want owner execute", script.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dst, "templates", "settings.json")); err != nil {
		t.Fatalf("nested file missing: %v", err)
	}
}

func TestCopyExtensionTreeSkipsGitDir(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	writeFixture(t, filepath.Join(src, "spec.yaml"), "x\n", 0o644)
	writeFixture(t, filepath.Join(src, ".git", "config"), "[core]\n", 0o644)

	if err := copyExtensionTree(src, dst, defaultLimits()); err != nil {
		t.Fatalf("copyExtensionTree: %v", err)
	}
	if got := countFiles(t, dst); got != 1 {
		t.Fatalf("copied %d files, want 1 (.git must be skipped)", got)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git was copied")
	}
}

// TestCopyExtensionTreeSkipsGitlinkFile: a submodule gitlink is a regular
// FILE named ".git", not a directory, so a directory-only skip would install
// it as ordinary extension content.
func TestCopyExtensionTreeSkipsGitlinkFile(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	writeFixture(t, filepath.Join(src, "spec.yaml"), "x\n", 0o644)
	writeFixture(t, filepath.Join(src, ".git"), "gitdir: ../.git/modules/sub\n", 0o644)

	if err := copyExtensionTree(src, dst, defaultLimits()); err != nil {
		t.Fatalf("copyExtensionTree: %v", err)
	}
	if got := countFiles(t, dst); got != 1 {
		t.Fatalf("copied %d files, want 1 (the .git gitlink file must be skipped)", got)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git gitlink file was copied")
	}
}

// TestCopyExtensionTreeRejectsSymlinkedSourceRoot: os.Stat follows symlinks,
// so a symlinked source root would be accepted as a real directory without
// ever proving it is one inside the checkout.
func TestCopyExtensionTreeRejectsSymlinkedSourceRoot(t *testing.T) {
	real := t.TempDir()
	writeFixture(t, filepath.Join(real, "spec.yaml"), "x\n", 0o644)
	linked := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, linked); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out")
	if err := copyExtensionTree(linked, dst, defaultLimits()); err == nil {
		t.Fatal("copyExtensionTree accepted a symlinked source root")
	}
}

func TestCopyExtensionTreeRejectsSymlink(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	writeFixture(t, filepath.Join(src, "spec.yaml"), "x\n", 0o644)
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "leak")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := copyExtensionTree(src, dst, defaultLimits())
	if err == nil {
		t.Fatal("copyExtensionTree accepted a symlink")
	}
	if !strings.Contains(err.Error(), "leak") {
		t.Fatalf("error does not name the offending path: %v", err)
	}
}

func TestCopyExtensionTreeEnforcesFileCap(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	for i := 0; i < 4; i++ {
		writeFixture(t, filepath.Join(src, string(rune('a'+i))+".txt"), "x\n", 0o644)
	}

	if err := copyExtensionTree(src, dst, copyLimits{MaxFiles: 2, MaxBytes: 1 << 20}); err == nil {
		t.Fatal("copyExtensionTree ignored MaxFiles")
	}
}

func TestCopyExtensionTreeEnforcesByteCap(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	writeFixture(t, filepath.Join(src, "big.bin"), strings.Repeat("x", 4096), 0o644)

	if err := copyExtensionTree(src, dst, copyLimits{MaxFiles: 10, MaxBytes: 1024}); err == nil {
		t.Fatal("copyExtensionTree ignored MaxBytes")
	}
}

func TestCopyExtensionTreeRequiresExistingSource(t *testing.T) {
	if err := copyExtensionTree(filepath.Join(t.TempDir(), "missing"), t.TempDir(), defaultLimits()); err == nil {
		t.Fatal("copyExtensionTree accepted a missing source")
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return count
}
