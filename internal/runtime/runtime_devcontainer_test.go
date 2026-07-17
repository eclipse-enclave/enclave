// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestAddDevcontainerEnvOmitsRemoteUserInAgentMode(t *testing.T) {
	r := &Runtime{
		devcontainer: &model.DevcontainerConfig{
			RemoteUser:        "node",
			PostCreateCommand: "npm ci",
			PostStartCommand:  "npm run dev",
			ContainerEnv: map[string]string{
				"NODE_ENV":            "development",
				"PROJECT_DIR":         "/skip",
				model.EnvPrefix + "X": "skip",
			},
		},
	}

	mounts := newMountAccumulator(nil, nil)
	r.addDevcontainerEnv(mounts)

	if !envSliceContainsKV(mounts.Env(), model.EnvDevcontainer, "1") {
		t.Fatalf("expected %s to be set", model.EnvDevcontainer)
	}
	if envSliceHasKey(mounts.Env(), model.EnvDevcontainerRemoteUser) {
		t.Fatalf("did not expect %s in agent mode", model.EnvDevcontainerRemoteUser)
	}
	if !envSliceContainsKV(mounts.Env(), model.EnvDevcontainerPostCreate, "npm ci") {
		t.Fatalf("expected %s to be set", model.EnvDevcontainerPostCreate)
	}
	if !envSliceContainsKV(mounts.Env(), model.EnvDevcontainerPostStart, "npm run dev") {
		t.Fatalf("expected %s to be set", model.EnvDevcontainerPostStart)
	}
	if !envSliceContainsKV(mounts.Env(), "NODE_ENV", "development") {
		t.Fatalf("expected containerEnv key to be included")
	}
	if envSliceHasKey(mounts.Env(), "PROJECT_DIR") {
		t.Fatalf("did not expect PROJECT_DIR override from containerEnv")
	}
	if envSliceHasKey(mounts.Env(), model.EnvPrefix+"X") {
		t.Fatalf("did not expect %s-prefixed key override from containerEnv", model.EnvPrefix)
	}
}

func TestAddDevcontainerEnvIncludesRemoteUserWhenApplied(t *testing.T) {
	r := &Runtime{
		build: model.BuildOptions{
			UseRemoteUser: true,
		},
		devcontainer: &model.DevcontainerConfig{
			RemoteUser: "node",
		},
		containerUser: "node",
	}

	mounts := newMountAccumulator(nil, nil)
	r.addDevcontainerEnv(mounts)

	if !envSliceContainsKV(mounts.Env(), model.EnvDevcontainerRemoteUser, "node") {
		t.Fatalf("expected %s to be set when remote user is applied", model.EnvDevcontainerRemoteUser)
	}
}

func TestAddDevcontainerEnvSkipsRemoteUserWhenUseRemoteUserNotResolved(t *testing.T) {
	r := &Runtime{
		build: model.BuildOptions{
			UseRemoteUser: true,
		},
		devcontainer: &model.DevcontainerConfig{
			RemoteUser: "node",
		},
		containerUser: model.ContainerUser,
	}

	mounts := newMountAccumulator(nil, nil)
	r.addDevcontainerEnv(mounts)

	if envSliceHasKey(mounts.Env(), model.EnvDevcontainerRemoteUser) {
		t.Fatalf("did not expect %s when remote user is not applied", model.EnvDevcontainerRemoteUser)
	}
}

func TestApplyDevcontainerUserIntentUseRemoteUserSetsUserAndHome(t *testing.T) {
	r := &Runtime{
		build: model.BuildOptions{
			UseRemoteUser: true,
		},
		devcontainer: &model.DevcontainerConfig{
			RemoteUser: "node",
		},
		containerUser: "node",
		containerHome: "/home/node",
	}

	var user string
	var env []string
	r.applyDevcontainerUserIntent(&user, &env)

	if user != "node" {
		t.Fatalf("expected user node, got %q", user)
	}
	if !envSliceContainsKV(env, "USER", "node") {
		t.Fatalf("expected USER to be set for remote user")
	}
	if !envSliceContainsKV(env, "HOME", "/home/node") {
		t.Fatalf("expected HOME to be set for remote user")
	}
}

func TestApplyRuntimeUIDRemapIntentRunsAsRootAndPassesHostIdentity(t *testing.T) {
	r := &Runtime{
		host:          model.Host{UID: "2000", GID: "3000"},
		build:         model.BuildOptions{RuntimeUIDRemap: true},
		containerUser: model.ContainerUser,
		containerHome: model.ContainerHome,
	}

	var user string
	var env []string
	r.applyRuntimeUIDRemapIntent(&user, &env)

	if user != "root" {
		t.Fatalf("expected root startup user, got %q", user)
	}
	if !envSliceContainsKV(env, model.EnvRuntimeUID, "2000") {
		t.Fatalf("expected %s env, got %v", model.EnvRuntimeUID, env)
	}
	if !envSliceContainsKV(env, model.EnvRuntimeGID, "3000") {
		t.Fatalf("expected %s env, got %v", model.EnvRuntimeGID, env)
	}
	if !envSliceContainsKV(env, "USER", model.ContainerUser) {
		t.Fatalf("expected USER env, got %v", env)
	}
	if !envSliceContainsKV(env, "HOME", model.ContainerHome) {
		t.Fatalf("expected HOME env, got %v", env)
	}
}

func TestApplyRuntimeUIDRemapIntentSkipsWhenDisabled(t *testing.T) {
	r := &Runtime{
		host:          model.Host{UID: "2000", GID: "3000"},
		containerUser: model.ContainerUser,
		containerHome: model.ContainerHome,
	}

	var user string
	var env []string
	r.applyRuntimeUIDRemapIntent(&user, &env)

	if user != "" {
		t.Fatalf("expected image user to remain default, got %q", user)
	}
	if len(env) != 0 {
		t.Fatalf("expected no env changes, got %v", env)
	}
}

func TestBaseMountsProjectMountReadonly(t *testing.T) {
	projectDir := t.TempDir()
	r := New(model.RuntimeConfig{
		Project: model.Project{Dir: projectDir, RealDir: projectDir},
		Run:     model.RunOptions{ProjectMount: model.ProjectMountReadonly},
	})

	got, env := r.baseMounts()
	if len(got) != 1 {
		t.Fatalf("expected one project mount, got %d: %+v", len(got), got)
	}
	if got[0].Source != projectDir || got[0].ContainerPath != projectDir {
		t.Fatalf("unexpected project mount: %+v", got[0])
	}
	if !got[0].ReadOnly {
		t.Fatalf("expected project mount to be read-only")
	}
	if !envSliceContainsKV(env, model.EnvProjectMount, model.ProjectMountReadonly) {
		t.Fatalf("expected %s=%s env, got %v", model.EnvProjectMount, model.ProjectMountReadonly, env)
	}
}

func TestBaseMountsProjectMountWritableByDefault(t *testing.T) {
	projectDir := t.TempDir()
	r := New(model.RuntimeConfig{
		Project: model.Project{Dir: projectDir, RealDir: projectDir},
	})

	got, _ := r.baseMounts()
	if len(got) != 1 {
		t.Fatalf("expected one project mount, got %d: %+v", len(got), got)
	}
	if got[0].ReadOnly {
		t.Fatalf("expected default project mount to be writable")
	}
}

func TestAddAdditionalMountsProjectMountReadonlyForcesProjectSubdirReadonly(t *testing.T) {
	projectDir := t.TempDir()
	projectSubdir := filepath.Join(projectDir, "src")
	if err := os.Mkdir(projectSubdir, 0o755); err != nil {
		t.Fatalf("mkdir project subdir: %v", err)
	}
	outsideDir := t.TempDir()
	r := &Runtime{
		project:       model.Project{Dir: projectDir, RealDir: projectDir},
		run:           model.RunOptions{ProjectMount: model.ProjectMountReadonly},
		validatedDirs: []string{projectSubdir, outsideDir},
	}

	mounts := newMountAccumulator(nil, nil)
	r.addAdditionalMounts(mounts)

	got := map[string]backend.Mount{}
	for _, m := range mounts.Mounts() {
		got[m.Source] = m
	}
	if len(got) != 2 {
		t.Fatalf("expected two additional mounts, got %d: %+v", len(got), got)
	}
	projectMount, ok := got[projectSubdir]
	if !ok {
		t.Fatalf("expected project subdir additional mount to be present: %+v", got)
	}
	if !projectMount.ReadOnly {
		t.Fatalf("expected project subdir additional mount to be read-only: %+v", projectMount)
	}
	outsideMount, ok := got[outsideDir]
	if !ok {
		t.Fatalf("expected outside additional mount to be present: %+v", got)
	}
	if outsideMount.ReadOnly {
		t.Fatalf("expected outside additional mount to remain writable: %+v", outsideMount)
	}
}

func TestAddWorktreeMetadataMounts(t *testing.T) {
	cases := []struct {
		name         string
		projectMount string
		metadata     string
		wantMounts   int
		wantReadOnly bool
	}{
		{name: "default follows writable project", wantMounts: 1, wantReadOnly: false},
		{name: "follow inherits readonly project", projectMount: model.ProjectMountReadonly, metadata: model.WorktreeMetadataFollow, wantMounts: 1, wantReadOnly: true},
		{name: "readonly forces read-only on writable project", metadata: model.WorktreeMetadataReadonly, wantMounts: 1, wantReadOnly: true},
		{name: "none skips metadata mounts", metadata: model.WorktreeMetadataNone, wantMounts: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatalf("resolve tempdir: %v", err)
			}
			projectDir := filepath.Join(root, "project")
			gitdir := filepath.Join(projectDir, ".gitwt", "wt-foo")
			if err := os.MkdirAll(gitdir, 0o755); err != nil {
				t.Fatalf("create gitdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(projectDir, ".git"), []byte("gitdir: ./.gitwt/wt-foo\n"), 0o644); err != nil {
				t.Fatalf("write .git: %v", err)
			}

			r := &Runtime{
				project: model.Project{Dir: projectDir, RealDir: projectDir},
				run:     model.RunOptions{ProjectMount: tc.projectMount, WorktreeMetadata: tc.metadata},
			}
			mounts := newMountAccumulator(nil, nil)
			r.addWorktreeMetadataMounts(mounts)

			got := mounts.Mounts()
			if len(got) != tc.wantMounts {
				t.Fatalf("expected %d worktree metadata mounts, got %d: %+v", tc.wantMounts, len(got), got)
			}
			if tc.wantMounts == 0 {
				return
			}
			if got[0].Source != gitdir {
				t.Fatalf("unexpected mount source: got %q want %q", got[0].Source, gitdir)
			}
			if got[0].ReadOnly != tc.wantReadOnly {
				t.Fatalf("gitdir mount ReadOnly = %t, want %t: %+v", got[0].ReadOnly, tc.wantReadOnly, got[0])
			}
		})
	}
}

func TestApplyDevcontainerUserIntentUseRemoteUserRespectsExistingHome(t *testing.T) {
	r := &Runtime{
		build: model.BuildOptions{
			UseRemoteUser: true,
		},
		devcontainer: &model.DevcontainerConfig{
			RemoteUser: "node",
		},
		containerUser: "node",
		containerHome: "/home/node",
	}

	user := ""
	env := []string{"HOME=/custom"}
	r.applyDevcontainerUserIntent(&user, &env)

	if user != "node" {
		t.Fatalf("expected user node, got %q", user)
	}
	if !envSliceContainsKV(env, "USER", "node") {
		t.Fatalf("expected USER to be set for remote user")
	}
	if !envSliceContainsKV(env, "HOME", "/custom") {
		t.Fatalf("expected existing HOME to remain unchanged")
	}
}

func TestApplyDevcontainerUserIntentUseRemoteUserRespectsExistingUserAndHome(t *testing.T) {
	r := &Runtime{
		build: model.BuildOptions{
			UseRemoteUser: true,
		},
		devcontainer: &model.DevcontainerConfig{
			RemoteUser: "node",
		},
		containerUser: "node",
		containerHome: "/home/node",
	}

	user := ""
	env := []string{"USER=custom", "HOME=/custom"}
	r.applyDevcontainerUserIntent(&user, &env)

	if user != "node" {
		t.Fatalf("expected user node, got %q", user)
	}
	if !envSliceContainsKV(env, "USER", "custom") {
		t.Fatalf("expected existing USER to remain unchanged")
	}
	if !envSliceContainsKV(env, "HOME", "/custom") {
		t.Fatalf("expected existing HOME to remain unchanged")
	}
}

func TestApplyDevcontainerUserIntentShellModeSetsUserAndHome(t *testing.T) {
	r := &Runtime{
		run: model.RunOptions{
			Shell: true,
		},
		devcontainer: &model.DevcontainerConfig{
			RemoteUser: "node",
		},
		userExistsCache: map[string]bool{
			"node": true,
		},
	}

	var user string
	var env []string
	r.applyDevcontainerUserIntent(&user, &env)

	if user != "node" {
		t.Fatalf("expected user node, got %q", user)
	}
	if !envSliceContainsKV(env, "USER", "node") {
		t.Fatalf("expected USER to be set for shell mode remote user")
	}
	if !envSliceContainsKV(env, "HOME", "/home/node") {
		t.Fatalf("expected HOME to be set for shell mode remote user")
	}
}

func TestAddDevcontainerMountsDropsSystemDirBindSource(t *testing.T) {
	r := &Runtime{
		project: model.Project{Dir: t.TempDir()},
		devcontainer: &model.DevcontainerConfig{
			Mounts: []string{"type=bind,source=/etc,target=/etc"},
		},
	}

	mounts := newMountAccumulator(nil, nil)
	r.addDevcontainerMounts(mounts)

	for _, m := range mounts.Mounts() {
		if m.Type == backend.MountTypeBind && m.Source == "/etc" {
			t.Fatalf("expected /etc bind mount to be dropped")
		}
	}
}

func TestAddDevcontainerMountsAllowsProjectBindSource(t *testing.T) {
	projectDir := t.TempDir()
	r := &Runtime{
		project: model.Project{Dir: projectDir},
		devcontainer: &model.DevcontainerConfig{
			Mounts: []string{"type=bind,source=" + projectDir + ",target=/workspace"},
		},
	}

	mounts := newMountAccumulator(nil, nil)
	r.addDevcontainerMounts(mounts)

	found := false
	for _, m := range mounts.Mounts() {
		if m.Type == backend.MountTypeBind && m.Source == projectDir && m.ContainerPath == "/workspace" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected project bind mount to be present, got %+v", mounts.Mounts())
	}
}

func TestAddDevcontainerMountsProjectMountReadonly(t *testing.T) {
	projectDir := t.TempDir()
	r := &Runtime{
		project: model.Project{Dir: projectDir},
		run:     model.RunOptions{ProjectMount: model.ProjectMountReadonly},
		devcontainer: &model.DevcontainerConfig{
			WorkspaceMount: "type=bind,source=" + projectDir + ",target=/workspace",
		},
	}

	mounts := newMountAccumulator(nil, nil)
	r.addDevcontainerMounts(mounts)

	got := mounts.Mounts()
	if len(got) != 1 {
		t.Fatalf("expected one devcontainer mount, got %+v", got)
	}
	if !got[0].ReadOnly {
		t.Fatalf("expected devcontainer workspace mount to be read-only: %+v", got[0])
	}
}

func TestApplyDevcontainerMountsRejectsBindSymlinkToSystemDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	projectDir := t.TempDir()
	symlinkPath := filepath.Join(projectDir, "creds")
	if err := os.Symlink("/etc", symlinkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	cases := []struct {
		name   string
		source string
	}{
		{name: "relative source", source: "creds"},
		{name: "absolute source", source: symlinkPath},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runtime{
				project: model.Project{Dir: projectDir},
				devcontainer: &model.DevcontainerConfig{
					Mounts: []string{"type=bind,source=" + tc.source + ",target=/x"},
				},
			}

			mounts := newMountAccumulator(nil, nil)
			r.addDevcontainerMounts(mounts)

			for _, m := range mounts.Mounts() {
				if m.Type == backend.MountTypeBind {
					t.Fatalf("expected symlink-to-/etc bind mount %q to be dropped, got %+v", tc.source, mounts.Mounts())
				}
			}
		})
	}
}

func TestApplyDevcontainerWorkspaceMountWithBadSourceIsBlocked(t *testing.T) {
	projectDir := t.TempDir()
	r := &Runtime{
		project: model.Project{Dir: projectDir},
		devcontainer: &model.DevcontainerConfig{
			WorkspaceMount: "type=bind,source=/etc,target=/workspaces/x",
		},
	}

	mounts := newMountAccumulator(nil, nil)
	r.addDevcontainerMounts(mounts)

	for _, m := range mounts.Mounts() {
		if m.Type == backend.MountTypeBind {
			t.Fatalf("expected workspaceMount source /etc to be dropped, got %+v", mounts.Mounts())
		}
	}
}

func TestApplyDevcontainerWorkspaceMountOutsideProjectIsBlocked(t *testing.T) {
	projectDir := t.TempDir()
	outside := t.TempDir()
	r := &Runtime{
		project: model.Project{Dir: projectDir},
		devcontainer: &model.DevcontainerConfig{
			WorkspaceMount: "type=bind,source=" + outside + ",target=/workspaces/x",
		},
	}

	mounts := newMountAccumulator(nil, nil)
	r.addDevcontainerMounts(mounts)

	for _, m := range mounts.Mounts() {
		if m.Type == backend.MountTypeBind {
			t.Fatalf("expected workspaceMount source outside project to be dropped, got %+v", mounts.Mounts())
		}
	}
}

func envSliceHasKey(env []string, key string) bool {
	return envHasKey(env, key)
}

func envSliceContainsKV(env []string, key string, value string) bool {
	target := key + "=" + value
	for _, item := range env {
		if item == target {
			return true
		}
	}
	return false
}
