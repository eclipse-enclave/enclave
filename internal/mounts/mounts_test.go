// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mounts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestValidateExtraDirsWithExistingSkipsDuplicate(t *testing.T) {
	dir := t.TempDir()

	got, err := ValidateExtraDirsWithExisting([]string{dir}, []string{dir}, "/project", "")
	if err != nil {
		t.Fatalf("ValidateExtraDirsWithExisting returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected duplicate directory to be skipped, got %v", got)
	}
}

func TestAddAdditionalSetsReadOnlyFlag(t *testing.T) {
	var mountArgs []backend.Mount
	AddAdditional(&mountArgs, []string{"/tmp/rw"}, false)
	AddAdditional(&mountArgs, []string{"/tmp/ro"}, true)

	if len(mountArgs) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(mountArgs))
	}
	if mountArgs[0].ReadOnly {
		t.Fatalf("expected first mount to be read-write")
	}
	if !mountArgs[1].ReadOnly {
		t.Fatalf("expected second mount to be read-only")
	}
}

func TestApplyProjectMountMode(t *testing.T) {
	t.Run("readonly forces bind mount read-only", func(t *testing.T) {
		mount := backend.Mount{Type: backend.MountTypeBind}
		ApplyProjectMountMode(&mount, model.ProjectMountReadonly)
		if !mount.ReadOnly {
			t.Fatalf("expected bind mount to be read-only")
		}
	})

	t.Run("writable mode does not downgrade read-only", func(t *testing.T) {
		mount := backend.Mount{Type: backend.MountTypeBind, ReadOnly: true}
		ApplyProjectMountMode(&mount, model.ProjectMountWritable)
		if !mount.ReadOnly {
			t.Fatalf("expected existing read-only flag to be preserved")
		}
	})

	t.Run("volume mount is unchanged", func(t *testing.T) {
		mount := backend.Mount{Type: backend.MountTypeVolume}
		ApplyProjectMountMode(&mount, model.ProjectMountReadonly)
		if mount.ReadOnly {
			t.Fatalf("expected non-bind mount to remain writable")
		}
	})

	t.Run("nil mount is ignored", func(t *testing.T) {
		ApplyProjectMountMode(nil, model.ProjectMountReadonly)
	})
}

// makeProject prepares a project dir at <root>/project with a `.git` regular
// file containing the given pointer body. Returns the model.Project that
// AddWorktree expects (Dir == RealDir, both canonicalized).
func makeProject(t *testing.T, gitFileContent string) (model.Project, string) {
	t.Helper()
	root := t.TempDir()
	// Resolve symlinks so RealDir matches what AddWorktree compares against.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve tempdir: %v", err)
	}
	projectDir := filepath.Join(resolvedRoot, "project")
	if err := os.Mkdir(projectDir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".git"), []byte(gitFileContent), 0o644); err != nil {
		t.Fatalf("write .git pointer: %v", err)
	}
	return model.Project{Dir: projectDir, RealDir: projectDir, Name: "project"}, resolvedRoot
}

func TestAddWorktree_RejectsGitdirOutsideProject_HomeSSH(t *testing.T) {
	// Simulate `gitdir: <somewhere outside project>` — use a sibling tmp dir
	// to stand in for `/home/<user>/.ssh` (we can't actually point at the user's
	// real .ssh in tests).
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve tempdir: %v", err)
	}
	outside := filepath.Join(resolvedRoot, "fake-ssh")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	projectDir := filepath.Join(resolvedRoot, "project")
	if err := os.Mkdir(projectDir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".git"), []byte("gitdir: "+outside+"\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}

	project := model.Project{Dir: projectDir, RealDir: projectDir, Name: "project"}
	var mountArgs []backend.Mount
	validated := []string{}
	AddWorktree(&mountArgs, project, &validated, false)

	if len(mountArgs) != 0 {
		t.Fatalf("expected no mounts for outside-project gitdir, got %v", mountArgs)
	}
}

func TestAddWorktree_RejectsGitdirInSystemDir(t *testing.T) {
	// `gitdir: /etc` — should be rejected even before the within-project check
	// because /etc is a system dir; but the within-project check fires first.
	// Either way, the mount must not be added.
	project, _ := makeProject(t, "gitdir: /etc\n")
	var mountArgs []backend.Mount
	validated := []string{}
	AddWorktree(&mountArgs, project, &validated, false)
	if len(mountArgs) != 0 {
		t.Fatalf("expected no mounts for /etc gitdir, got %v", mountArgs)
	}
}

func TestAddWorktree_AllowsVerifiedExternalLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve tempdir: %v", err)
	}
	projectDir := filepath.Join(resolvedRoot, "project")
	commonDir := filepath.Join(resolvedRoot, "main", ".git")
	gitdir := filepath.Join(commonDir, "worktrees", "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatalf("create gitdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatalf("write project .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "gitdir"), []byte(filepath.Join(projectDir, ".git")+"\n"), 0o644); err != nil {
		t.Fatalf("write gitdir back pointer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatalf("write commondir: %v", err)
	}

	project := model.Project{Dir: projectDir, RealDir: projectDir, Name: "project"}
	var mountArgs []backend.Mount
	validated := []string{}
	AddWorktree(&mountArgs, project, &validated, false)

	if len(mountArgs) != 2 {
		t.Fatalf("expected gitdir + commondir mounts, got %d: %v", len(mountArgs), mountArgs)
	}
	for i, m := range mountArgs {
		if m.ReadOnly {
			t.Fatalf("expected mount %d to be writable: %v", i, m)
		}
	}
	if mountArgs[0].Source != gitdir {
		t.Fatalf("gitdir mount source: got %q want %q", mountArgs[0].Source, gitdir)
	}
	if mountArgs[1].Source != commonDir {
		t.Fatalf("commondir mount source: got %q want %q", mountArgs[1].Source, commonDir)
	}
}

func TestAddWorktree_RejectsForgedExternalWorktreeForDifferentProject(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve tempdir: %v", err)
	}
	attackerDir := filepath.Join(resolvedRoot, "attacker")
	victimDir := filepath.Join(resolvedRoot, "victim")
	commonDir := filepath.Join(resolvedRoot, "main", ".git")
	victimGitdir := filepath.Join(commonDir, "worktrees", "victim")
	if err := os.MkdirAll(attackerDir, 0o755); err != nil {
		t.Fatalf("create attacker dir: %v", err)
	}
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatalf("create victim dir: %v", err)
	}
	if err := os.MkdirAll(victimGitdir, 0o755); err != nil {
		t.Fatalf("create victim gitdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(attackerDir, ".git"), []byte("gitdir: "+victimGitdir+"\n"), 0o644); err != nil {
		t.Fatalf("write attacker .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(victimDir, ".git"), []byte("gitdir: "+victimGitdir+"\n"), 0o644); err != nil {
		t.Fatalf("write victim .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(victimGitdir, "gitdir"), []byte(filepath.Join(victimDir, ".git")+"\n"), 0o644); err != nil {
		t.Fatalf("write victim gitdir back pointer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(victimGitdir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatalf("write commondir: %v", err)
	}

	project := model.Project{Dir: attackerDir, RealDir: attackerDir, Name: "attacker"}
	var mountArgs []backend.Mount
	validated := []string{}
	AddWorktree(&mountArgs, project, &validated, false)

	if len(mountArgs) != 0 {
		t.Fatalf("expected no mounts for forged external worktree pointer, got %v", mountArgs)
	}
}

func TestAddWorktree_RejectsSymlinkedGitFile(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve tempdir: %v", err)
	}
	victimDir := filepath.Join(resolvedRoot, "victim")
	attackerDir := filepath.Join(resolvedRoot, "attacker")
	commonDir := filepath.Join(resolvedRoot, "main", ".git")
	victimGitdir := filepath.Join(commonDir, "worktrees", "victim")
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatalf("create victim dir: %v", err)
	}
	if err := os.MkdirAll(attackerDir, 0o755); err != nil {
		t.Fatalf("create attacker dir: %v", err)
	}
	if err := os.MkdirAll(victimGitdir, 0o755); err != nil {
		t.Fatalf("create victim gitdir: %v", err)
	}
	victimGitFile := filepath.Join(victimDir, ".git")
	if err := os.WriteFile(victimGitFile, []byte("gitdir: "+victimGitdir+"\n"), 0o644); err != nil {
		t.Fatalf("write victim .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(victimGitdir, "gitdir"), []byte(victimGitFile+"\n"), 0o644); err != nil {
		t.Fatalf("write victim gitdir back pointer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(victimGitdir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatalf("write commondir: %v", err)
	}
	if err := os.Symlink(victimGitFile, filepath.Join(attackerDir, ".git")); err != nil {
		t.Fatalf("symlink attacker .git: %v", err)
	}

	project := model.Project{Dir: attackerDir, RealDir: attackerDir, Name: "attacker"}
	var mountArgs []backend.Mount
	validated := []string{}
	AddWorktree(&mountArgs, project, &validated, false)

	if len(mountArgs) != 0 {
		t.Fatalf("expected no mounts for symlinked .git file, got %v", mountArgs)
	}
}

func TestReadRegularFileInDirRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(strings.Repeat("x", maxWorktreeMetadataBytes+1)), 0o644); err != nil {
		t.Fatalf("write oversized .git: %v", err)
	}

	if _, err := readRegularFileInDir(dir, ".git"); err == nil {
		t.Fatalf("expected oversized metadata file to be rejected")
	}
}

func TestAddWorktree_AllowsLegitimateGitdirInsideProject(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve tempdir: %v", err)
	}
	projectDir := filepath.Join(resolvedRoot, "project")
	gitdir := filepath.Join(projectDir, ".gitwt", "wt-foo")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatalf("create gitdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".git"), []byte("gitdir: ./.gitwt/wt-foo\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}

	project := model.Project{Dir: projectDir, RealDir: projectDir, Name: "project"}
	var mountArgs []backend.Mount
	validated := []string{}
	AddWorktree(&mountArgs, project, &validated, false)

	if len(mountArgs) != 1 {
		t.Fatalf("expected exactly one gitdir mount, got %d: %v", len(mountArgs), mountArgs)
	}
	if mountArgs[0].Source != gitdir {
		t.Fatalf("unexpected mount source: got %q want %q", mountArgs[0].Source, gitdir)
	}
	if mountArgs[0].ReadOnly {
		t.Fatalf("expected gitdir mount to be writable")
	}
}

func TestAddWorktree_ReadOnlyMountsMetadata(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve tempdir: %v", err)
	}
	projectDir := filepath.Join(resolvedRoot, "project")
	gitdir := filepath.Join(projectDir, ".gitwt", "wt-foo")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatalf("create gitdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".git"), []byte("gitdir: ./.gitwt/wt-foo\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}

	project := model.Project{Dir: projectDir, RealDir: projectDir, Name: "project"}
	var mountArgs []backend.Mount
	validated := []string{}
	AddWorktree(&mountArgs, project, &validated, true)

	if len(mountArgs) != 1 {
		t.Fatalf("expected exactly one gitdir mount, got %d: %v", len(mountArgs), mountArgs)
	}
	if mountArgs[0].Source != gitdir {
		t.Fatalf("unexpected mount source: got %q want %q", mountArgs[0].Source, gitdir)
	}
	if !mountArgs[0].ReadOnly {
		t.Fatalf("expected gitdir mount to be read-only")
	}
}

func TestAddWorktree_RejectsCommondirOutsideProject(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve tempdir: %v", err)
	}
	projectDir := filepath.Join(resolvedRoot, "project")
	gitdir := filepath.Join(projectDir, ".evil", "gitdir")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatalf("create gitdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".git"), []byte("gitdir: ./.evil/gitdir\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}
	// commondir points to /etc — outside project AND a system dir.
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("/etc\n"), 0o644); err != nil {
		t.Fatalf("write commondir: %v", err)
	}

	project := model.Project{Dir: projectDir, RealDir: projectDir, Name: "project"}
	var mountArgs []backend.Mount
	validated := []string{}
	AddWorktree(&mountArgs, project, &validated, false)

	// The benign in-project gitdir should still be mounted, but commondir must not.
	if len(mountArgs) != 1 {
		t.Fatalf("expected exactly one mount (gitdir only), got %d: %v", len(mountArgs), mountArgs)
	}
	if mountArgs[0].Source != gitdir {
		t.Fatalf("unexpected mount source: got %q want %q", mountArgs[0].Source, gitdir)
	}
	if mountArgs[0].ReadOnly {
		t.Fatalf("expected gitdir mount to be writable")
	}
}

func TestAddWorktree_AllowsCommondirInsideProject(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve tempdir: %v", err)
	}
	projectDir := filepath.Join(resolvedRoot, "project")
	gitdir := filepath.Join(projectDir, ".gitwt", "wt-foo")
	commondir := filepath.Join(projectDir, ".gitwt", "common")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatalf("create gitdir: %v", err)
	}
	if err := os.MkdirAll(commondir, 0o755); err != nil {
		t.Fatalf("create commondir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".git"), []byte("gitdir: ./.gitwt/wt-foo\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}
	// commondir contents: a relative path resolved against gitdir.
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("../common\n"), 0o644); err != nil {
		t.Fatalf("write commondir: %v", err)
	}

	project := model.Project{Dir: projectDir, RealDir: projectDir, Name: "project"}
	var mountArgs []backend.Mount
	validated := []string{}
	AddWorktree(&mountArgs, project, &validated, false)

	if len(mountArgs) != 2 {
		t.Fatalf("expected gitdir + commondir mounts, got %d: %v", len(mountArgs), mountArgs)
	}
	for i, m := range mountArgs {
		if m.ReadOnly {
			t.Fatalf("expected mount %d to be writable: %v", i, m)
		}
	}
	if mountArgs[0].Source != gitdir {
		t.Fatalf("gitdir mount source: got %q want %q", mountArgs[0].Source, gitdir)
	}
	if mountArgs[1].Source != commondir {
		t.Fatalf("commondir mount source: got %q want %q", mountArgs[1].Source, commondir)
	}
}
