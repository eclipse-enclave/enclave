// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveProjectFromDir_LinkedWorktreeUsesOwnHash(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	tmp := t.TempDir()
	mainDir := filepath.Join(tmp, "repo")
	linkedDir := filepath.Join(tmp, "repo-feature")

	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatalf("create main dir: %v", err)
	}

	runGitInDir(t, tmp, "init", mainDir)
	runGitInDir(t, mainDir, "config", "user.email", "test@example.com")
	runGitInDir(t, mainDir, "config", "user.name", "Test User")

	readme := filepath.Join(mainDir, "README.md")
	if err := os.WriteFile(readme, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	runGitInDir(t, mainDir, "add", "README.md")
	runGitInDir(t, mainDir, "commit", "-m", "init")

	runGitInDir(t, mainDir, "worktree", "add", "-b", "feature", linkedDir)

	mainProject, err := ResolveProjectFromDir(mainDir)
	if err != nil {
		t.Fatalf("resolve main project: %v", err)
	}
	linkedProject, err := ResolveProjectFromDir(linkedDir)
	if err != nil {
		t.Fatalf("resolve linked project: %v", err)
	}

	if mainProject.Hash == "" {
		t.Fatalf("main project hash should not be empty")
	}
	// Linked worktrees use a regular `.git` file pointer, which is untrusted
	// for hashing because it can be forged to inherit another project's hash
	// (see TestResolveProjectFromDir_GitPointerSpoofing). Each worktree's hash
	// is derived from its own RealDir.
	if linkedProject.Hash == mainProject.Hash {
		t.Fatalf("expected linked worktree hash to differ from main hash; both were %q", linkedProject.Hash)
	}
	if linkedProject.Dir != linkedDir {
		t.Fatalf("expected linked project dir %q, got %q", linkedDir, linkedProject.Dir)
	}
	if linkedProject.RealDir == mainProject.RealDir {
		t.Fatalf("expected linked real dir %q to differ from main real dir %q", linkedProject.RealDir, mainProject.RealDir)
	}
}

// TestResolveProjectFromDir_GitPointerSpoofing verifies that a malicious repo
// with a forged `.git` regular file cannot inherit another project's hash.
// Without the fix, `git worktree list` would follow the pointer and report
// the victim repo's main path as worktrees[0], causing the malicious project
// to compute the victim's hash and silently mount its persisted state
// (auth stores, generated config, etc.).
func TestResolveProjectFromDir_GitPointerSpoofing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	tmp := t.TempDir()
	victimDir := filepath.Join(tmp, "victim")
	attackerDir := filepath.Join(tmp, "attacker")

	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatalf("create victim dir: %v", err)
	}
	if err := os.MkdirAll(attackerDir, 0o755); err != nil {
		t.Fatalf("create attacker dir: %v", err)
	}

	// Set up a real repo with a worktree as the victim.
	runGitInDir(t, tmp, "init", victimDir)
	runGitInDir(t, victimDir, "config", "user.email", "test@example.com")
	runGitInDir(t, victimDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(victimDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	runGitInDir(t, victimDir, "add", "README.md")
	runGitInDir(t, victimDir, "commit", "-m", "init")
	victimLinked := filepath.Join(tmp, "victim-feature")
	runGitInDir(t, victimDir, "worktree", "add", "-b", "feature", victimLinked)

	// Forge `.git` in the attacker repo to point at the victim's worktree gitdir.
	pointer := "gitdir: " + filepath.Join(victimDir, ".git", "worktrees", "victim-feature") + "\n"
	if err := os.WriteFile(filepath.Join(attackerDir, ".git"), []byte(pointer), 0o644); err != nil {
		t.Fatalf("write attacker .git pointer: %v", err)
	}

	victimProject, err := ResolveProjectFromDir(victimDir)
	if err != nil {
		t.Fatalf("resolve victim project: %v", err)
	}
	attackerProject, err := ResolveProjectFromDir(attackerDir)
	if err != nil {
		t.Fatalf("resolve attacker project: %v", err)
	}

	if attackerProject.Hash == victimProject.Hash {
		t.Fatalf("attacker project must not inherit victim hash (%q)", victimProject.Hash)
	}
}

func TestResolveProjectFromDir_GitDirSymlinkSpoofing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	tmp := t.TempDir()
	victimDir := filepath.Join(tmp, "victim")
	attackerDir := filepath.Join(tmp, "attacker")

	if err := os.MkdirAll(attackerDir, 0o755); err != nil {
		t.Fatalf("create attacker dir: %v", err)
	}

	runGitInDir(t, tmp, "init", victimDir)
	runGitInDir(t, victimDir, "config", "user.email", "test@example.com")
	runGitInDir(t, victimDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(victimDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	runGitInDir(t, victimDir, "add", "README.md")
	runGitInDir(t, victimDir, "commit", "-m", "init")

	if err := os.Symlink(filepath.Join(victimDir, ".git"), filepath.Join(attackerDir, ".git")); err != nil {
		t.Fatalf("symlink attacker .git: %v", err)
	}

	victimProject, err := ResolveProjectFromDir(victimDir)
	if err != nil {
		t.Fatalf("resolve victim project: %v", err)
	}
	attackerProject, err := ResolveProjectFromDir(attackerDir)
	if err != nil {
		t.Fatalf("resolve attacker project: %v", err)
	}

	if attackerProject.Hash == victimProject.Hash {
		t.Fatalf("attacker project must not inherit victim hash through .git symlink (%q)", victimProject.Hash)
	}
}

func runGitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}
