// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runCopyWorkspaceFiles sources kit-init.sh and calls
// enclave_copy_workspace_files "$EXT" with PROJECT_DIR set to project.
func runCopyWorkspaceFiles(t *testing.T, ext, project string) (string, error) {
	t.Helper()
	script := `set -e; . "$KIT"; enclave_copy_workspace_files "$EXT"`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"KIT=" + kitInitScript(t),
		"EXT=" + ext,
		"PROJECT_DIR=" + project,
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// seedWorkspaceFile writes content to path (parents created) with mode.
func seedWorkspaceFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func TestCopyWorkspaceFilesCopiesMissing(t *testing.T) {
	ext := t.TempDir()
	project := t.TempDir()

	seedWorkspaceFile(t, filepath.Join(ext, "files", "workspace", "docs", "guide.md"), "hi\n", 0o644)
	seedWorkspaceFile(t, filepath.Join(ext, "files", "workspace", "hooks", "run.sh"), "#!/bin/sh\n", 0o755)

	if out, err := runCopyWorkspaceFiles(t, ext, project); err != nil {
		t.Fatalf("copy workspace files: %v\n%s", err, out)
	}

	got, err := os.ReadFile(filepath.Join(project, "docs", "guide.md"))
	if err != nil {
		t.Fatalf("read guide: %v", err)
	}
	if string(got) != "hi\n" {
		t.Fatalf("guide = %q, want %q", string(got), "hi\n")
	}

	info, err := os.Stat(filepath.Join(project, "hooks", "run.sh"))
	if err != nil {
		t.Fatalf("stat run.sh: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Fatalf("run.sh perm = %o, want 0755 (mode must be preserved)", perm)
	}
}

func TestCopyWorkspaceFilesNeverClobbers(t *testing.T) {
	ext := t.TempDir()
	project := t.TempDir()

	seedWorkspaceFile(t, filepath.Join(ext, "files", "workspace", "keep.txt"), "KIT\n", 0o644)
	// Pre-existing host file must be preserved verbatim.
	seedWorkspaceFile(t, filepath.Join(project, "keep.txt"), "HOST\n", 0o644)

	out, err := runCopyWorkspaceFiles(t, ext, project)
	if err != nil {
		t.Fatalf("copy workspace files: %v\n%s", err, out)
	}

	got, err := os.ReadFile(filepath.Join(project, "keep.txt"))
	if err != nil {
		t.Fatalf("read keep.txt: %v", err)
	}
	if string(got) != "HOST\n" {
		t.Fatalf("keep.txt = %q, want %q (host file must not be clobbered)", string(got), "HOST\n")
	}
	if !strings.Contains(out, "keep.txt") || !strings.Contains(strings.ToLower(out), "not overwriting") {
		t.Fatalf("expected a 'not overwriting' warning mentioning keep.txt, got:\n%s", out)
	}
}

func TestCopyWorkspaceFilesDoesNotFollowDanglingSymlink(t *testing.T) {
	ext := t.TempDir()
	project := t.TempDir()

	seedWorkspaceFile(t, filepath.Join(ext, "files", "workspace", "hooks", "run.sh"), "KIT\n", 0o644)
	// A dangling symlink at the destination path: `-e` reports it absent, so a
	// naive copy would create the symlink's target with kit content. The guard
	// must skip it and leave the target uncreated.
	target := filepath.Join(project, "escape-target")
	if err := os.Symlink(target, filepath.Join(project, "hooks-link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	// Point the file's parent dir through the symlink by naming the rel path to
	// collide with a symlinked dest. Simpler: symlink the dest file directly.
	linkDest := filepath.Join(project, "hooks", "run.sh")
	if err := os.MkdirAll(filepath.Dir(linkDest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, linkDest); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	out, err := runCopyWorkspaceFiles(t, ext, project)
	if err != nil {
		t.Fatalf("copy workspace files: %v\n%s", err, out)
	}
	if _, err := os.Lstat(target); err == nil {
		t.Fatalf("kit content was written through a dangling symlink to %s", target)
	}
}

func TestCopyWorkspaceFilesFailureIsNonFatal(t *testing.T) {
	ext := t.TempDir()
	project := t.TempDir()

	// Two files; a symlinked destination collision on one must not stop the
	// other from copying, and the whole call must exit 0 (never abort startup).
	seedWorkspaceFile(t, filepath.Join(ext, "files", "workspace", "blocked", "x.txt"), "KIT\n", 0o644)
	seedWorkspaceFile(t, filepath.Join(ext, "files", "workspace", "ok.txt"), "OK\n", 0o644)
	if err := os.Symlink("/nonexistent/target", filepath.Join(project, "blocked")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	out, err := runCopyWorkspaceFiles(t, ext, project)
	if err != nil {
		t.Fatalf("expected non-fatal copy, got error: %v\n%s", err, out)
	}
	if got, err := os.ReadFile(filepath.Join(project, "ok.txt")); err != nil || string(got) != "OK\n" {
		t.Fatalf("sibling file must still copy; got %q err %v", got, err)
	}
}

func TestCopyWorkspaceFilesNoDirNoop(t *testing.T) {
	ext := t.TempDir()
	project := t.TempDir()

	if out, err := runCopyWorkspaceFiles(t, ext, project); err != nil {
		t.Fatalf("no files/workspace should be a clean no-op: %v\n%s", err, out)
	}
	entries, err := os.ReadDir(project)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("project should be untouched, got %d entries", len(entries))
	}
}

func TestCopyWorkspaceFilesEmptyProjectDirNoop(t *testing.T) {
	ext := t.TempDir()
	seedWorkspaceFile(t, filepath.Join(ext, "files", "workspace", "a.txt"), "a\n", 0o644)

	// Empty PROJECT_DIR must be a no-op (nothing to copy into).
	if out, err := runCopyWorkspaceFiles(t, ext, ""); err != nil {
		t.Fatalf("empty PROJECT_DIR should be a no-op: %v\n%s", err, out)
	}
}
