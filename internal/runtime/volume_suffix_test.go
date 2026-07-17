// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"path/filepath"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

func TestWorktreeConfigVolumeSuffixUsesSiblingWorktreeHashWhenConfigSourceIsActive(t *testing.T) {
	t.Parallel()

	mainProject := model.Project{RealDir: "/tmp/repo"}
	mainHash := projectPathHash(mainProject)

	r := &Runtime{
		project: model.Project{
			Dir:     "/tmp/repo-feature",
			RealDir: "/tmp/repo-feature",
			Hash:    mainHash,
		},
		profile: model.Profile{Name: "claude", ConfigDir: ".claude"},
		run:     model.RunOptions{HostConfig: model.HostConfigPassthrough},
	}

	got := r.worktreeConfigVolumeSuffix()
	want := projectPathHash(r.project)
	if got != want {
		t.Fatalf("worktreeConfigVolumeSuffix() = %q, want %q", got, want)
	}
}

func TestWorktreeConfigVolumeSuffixSkipsMainWorktree(t *testing.T) {
	t.Parallel()

	project := model.Project{
		Dir:     "/tmp/repo",
		RealDir: "/tmp/repo",
	}
	project.Hash = projectPathHash(project)

	r := &Runtime{
		project: project,
		profile: model.Profile{Name: "claude", ConfigDir: ".claude"},
		run:     model.RunOptions{HostConfig: model.HostConfigPassthrough},
	}

	if got := r.worktreeConfigVolumeSuffix(); got != "" {
		t.Fatalf("worktreeConfigVolumeSuffix() = %q, want empty suffix", got)
	}
}

func TestWorktreeConfigVolumeSuffixSkipsWhenConfigSourceIsInactive(t *testing.T) {
	t.Parallel()

	mainProject := model.Project{RealDir: "/tmp/repo"}
	mainHash := projectPathHash(mainProject)

	r := &Runtime{
		project: model.Project{
			Dir:     "/tmp/repo-feature",
			RealDir: "/tmp/repo-feature",
			Hash:    mainHash,
		},
		profile: model.Profile{Name: "claude", ConfigDir: ".claude"},
	}

	if got := r.worktreeConfigVolumeSuffix(); got != "" {
		t.Fatalf("worktreeConfigVolumeSuffix() = %q, want empty suffix", got)
	}
}

func TestConfigSourceChownSpec(t *testing.T) {
	t.Parallel()

	if got := configSourceChownSpec("1000", "1001"); got != "1000:1001" {
		t.Fatalf("configSourceChownSpec() = %q, want %q", got, "1000:1001")
	}
	if got := configSourceChownSpec("", "1001"); got != "" {
		t.Fatalf("configSourceChownSpec() = %q, want empty result for invalid uid", got)
	}
	if got := configSourceChownSpec("1000", "-1"); got != "" {
		t.Fatalf("configSourceChownSpec() = %q, want empty result for invalid gid", got)
	}
}

func TestCurrentConfigVolumeSuffixUsesContainerSuffixForConcurrentUnnamedSession(t *testing.T) {
	r := &Runtime{
		project: model.Project{
			Dir:     "/tmp/repo",
			RealDir: "/tmp/repo",
			Hash:    "abc123abc123",
		},
		profile: model.Profile{Name: "claude", ConfigDir: ".claude"},
		run:     model.RunOptions{Persist: true},
	}

	r.backend = &fakeBackend{configKeyInUse: func(meta backend.SessionMeta, key string) (bool, error) {
		return meta.Tool == "claude" && meta.ProjectHash == "abc123abc123" &&
			meta.Worktree == "/tmp/repo" && key == defaultConfigKey, nil
	}}

	got := r.currentConfigVolumeSuffix("enclave-claude-abc123abc123-2", "enclave-claude-abc123abc123")
	if got != "2" {
		t.Fatalf("currentConfigVolumeSuffix() = %q, want %q", got, "2")
	}
}

func TestCurrentConfigVolumeSuffixKeepsStableWorktreeKeyWhenOnlyOtherWorktreeIsRunning(t *testing.T) {
	mainProject := model.Project{RealDir: "/tmp/repo"}
	mainHash := projectPathHash(mainProject)

	r := &Runtime{
		project: model.Project{
			Dir:     "/tmp/repo-feature",
			RealDir: "/tmp/repo-feature",
			Hash:    mainHash,
		},
		profile: model.Profile{Name: "claude", ConfigDir: ".claude"},
		run:     model.RunOptions{HostConfig: model.HostConfigPassthrough, Persist: true},
	}

	// The only running session is on a different worktree, so its config key
	// does not conflict with this worktree's stable key.
	r.backend = &fakeBackend{configKeyInUse: func(meta backend.SessionMeta, key string) (bool, error) {
		return meta.Worktree == "/tmp/repo" && key == defaultConfigKey, nil
	}}

	got := r.currentConfigVolumeSuffix("enclave-claude-"+mainHash+"-2", "enclave-claude-"+mainHash)
	want := projectPathHash(r.project)
	if got != want {
		t.Fatalf("currentConfigVolumeSuffix() = %q, want %q", got, want)
	}
}

func TestGeneratedConfigSourceDirDefaultsToDefaultKey(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		host:    model.Host{Home: "/tmp/home"},
		project: model.Project{Hash: "abc123abc123"},
		profile: model.Profile{Name: "claude"},
	}

	got := r.generatedConfigSourceDir()
	want := filepath.Join(config.HostProjectGeneratedConfigDir("/tmp/home", "abc123abc123", "claude"), "default")
	if got != want {
		t.Fatalf("generatedConfigSourceDir() = %q, want %q", got, want)
	}
}

func TestGeneratedConfigSourceDirUsesConfigVolumeSuffix(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		host:            model.Host{Home: "/tmp/home"},
		project:         model.Project{Hash: "abc123abc123"},
		profile:         model.Profile{Name: "claude"},
		configVolSuffix: "worktreehash",
		configVolReady:  true,
	}

	got := r.generatedConfigSourceDir()
	want := filepath.Join(config.HostProjectGeneratedConfigDir("/tmp/home", "abc123abc123", "claude"), "worktreehash")
	if got != want {
		t.Fatalf("generatedConfigSourceDir() = %q, want %q", got, want)
	}
}
