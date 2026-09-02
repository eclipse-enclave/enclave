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
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

func TestTaggedProjectUsesSharedDefaultConfigStore(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		project: model.Project{
			Dir:     "/tmp/repo-feature",
			RealDir: "/tmp/repo-feature",
			Hash:    "abc123abc123",
		},
		profile: model.Profile{Name: "claude", ConfigDir: ".claude"},
		run:     model.RunOptions{HostConfig: model.HostConfigPassthrough},
	}

	if got := r.deriveConfigVolumeSuffix("enclave-claude-abc123abc123", "enclave-claude-abc123abc123"); got != "" {
		t.Fatalf("deriveConfigVolumeSuffix() = %q, want shared default store", got)
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

func TestCurrentConfigVolumeSuffixIsolatesConcurrentTaggedMember(t *testing.T) {
	r := &Runtime{
		project: model.Project{
			Dir:     "/tmp/repo-feature",
			RealDir: "/tmp/repo-feature",
			Hash:    "abc123abc123",
		},
		profile: model.Profile{Name: "claude", ConfigDir: ".claude"},
		run:     model.RunOptions{HostConfig: model.HostConfigPassthrough, Persist: true},
	}

	r.backend = &fakeBackend{configKeyInUse: func(meta backend.SessionMeta, key string) (bool, error) {
		return meta.ProjectHash == "abc123abc123" && key == defaultConfigKey, nil
	}}

	got := r.currentConfigVolumeSuffix("enclave-claude-abc123abc123-2", "enclave-claude-abc123abc123")
	if got != "2" {
		t.Fatalf("currentConfigVolumeSuffix() = %q, want concurrent session suffix", got)
	}
}

func TestConfigStoreLeaseRejectsConflictWhenBackendCannotListSessions(t *testing.T) {
	home := t.TempDir()
	newRuntime := func(projectDir string) *Runtime {
		r := &Runtime{
			host:    model.Host{Home: home},
			project: model.Project{Dir: projectDir, RealDir: projectDir, Hash: "abc123abc123"},
			profile: model.Profile{Name: "claude", ConfigDir: ".claude"},
			run:     model.RunOptions{Persist: true},
			backend: &fakeBackend{listFn: func(backend.SessionFilter) ([]backend.Session, error) { return nil, nil }},
		}
		base := r.baseContainerName()
		r.setConfigVolumeSuffix(base, base)
		return r
	}

	first := newRuntime("/tmp/repo")
	firstLease, err := first.acquireConfigStoreLease()
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	defer func() { _ = firstLease.Release() }()

	second := newRuntime("/tmp/repo-feature")
	if conflictingLease, err := second.acquireConfigStoreLease(); err == nil {
		_ = conflictingLease.Release()
		t.Fatal("expected concurrent default-store lease to be rejected")
	} else if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("unexpected lease conflict error: %v", err)
	}
	storeDir := config.HostStoreConfigDir(home, "claude", "abc123abc123", defaultConfigKey)
	if _, err := os.Stat(storeDir); !os.IsNotExist(err) {
		t.Fatalf("conflicting lease touched config store %s: %v", storeDir, err)
	}

	_ = firstLease.Release()
	third := newRuntime("/tmp/repo-feature")
	thirdLease, err := third.acquireConfigStoreLease()
	if err != nil {
		t.Fatalf("acquire lease after release: %v", err)
	}
	_ = thirdLease.Release()
}

func TestConfigStoreLeaseCoordinatesBackgroundSessions(t *testing.T) {
	home := t.TempDir()
	newRuntime := func(background bool) *Runtime {
		return &Runtime{
			host:            model.Host{Home: home},
			project:         model.Project{Dir: "/tmp/repo", RealDir: "/tmp/repo", Hash: "abc123abc123"},
			profile:         model.Profile{Name: "claude", ConfigDir: ".claude"},
			run:             model.RunOptions{Persist: true, Background: background, SessionName: "shared"},
			configVolSuffix: "shared",
			configVolReady:  true,
		}
	}

	foregroundLease, err := newRuntime(false).acquireConfigStoreLease()
	if err != nil {
		t.Fatalf("acquire foreground lease: %v", err)
	}
	if conflictingLease, err := newRuntime(true).acquireConfigStoreLease(); err == nil {
		_ = conflictingLease.Release()
		t.Fatal("expected background session to observe foreground lease")
	}
	_ = foregroundLease.Release()

	backgroundLease, err := newRuntime(true).acquireConfigStoreLease()
	if err != nil {
		t.Fatalf("acquire background lease: %v", err)
	}
	defer func() { _ = backgroundLease.Release() }()
	if conflictingLease, err := newRuntime(false).acquireConfigStoreLease(); err == nil {
		_ = conflictingLease.Release()
		t.Fatal("expected foreground session to observe background lease")
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
