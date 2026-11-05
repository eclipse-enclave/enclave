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
		return meta.Tool == "claude" && meta.ProjectHash == "abc123abc123" && key == defaultConfigKey, nil
	}}

	got := r.currentConfigVolumeSuffix("enclave-claude-abc123abc123-2", "enclave-claude-abc123abc123")
	if got != "2" {
		t.Fatalf("currentConfigVolumeSuffix() = %q, want %q", got, "2")
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
		configVolSuffix: "review",
		configVolReady:  true,
	}

	got := r.generatedConfigSourceDir()
	want := filepath.Join(config.HostProjectGeneratedConfigDir("/tmp/home", "abc123abc123", "claude"), "review")
	if got != want {
		t.Fatalf("generatedConfigSourceDir() = %q, want %q", got, want)
	}
}
