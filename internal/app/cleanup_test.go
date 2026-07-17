// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

func TestResolveCleanupDirs(t *testing.T) {
	t.Parallel()

	home := "/tmp/test-home"
	project := model.Project{Hash: "projhash"}
	run := model.RunOptions{Tool: "codex"}
	dirs := resolveCleanupDirs(run, model.CleanupOptions{}, home, project)

	expected := map[string]bool{
		config.HostCacheToolProjectDir(home, run.Tool, project.Hash):                                         true,
		filepath.Join(config.HostProjectToolDir(home, project.Hash, run.Tool), "history"):                    true,
		config.HostProjectHomeConfigDir(home, project.Hash, run.Tool):                                        true,
		config.HostProjectGeneratedConfigDir(home, project.Hash, run.Tool):                                   true,
		filepath.Join(config.HostProjectToolDir(home, project.Hash, run.Tool), model.GeneratedSkillsDirName): true,
		config.HostStoreConfigRootDir(home, run.Tool, project.Hash):                                          true,
		config.HostStoreEnvDir(home, run.Tool, project.Hash):                                                 true,
		config.HostProjectMemoryDir(home, project.Hash, run.Tool):                                            true,
	}

	if len(dirs) != len(expected) {
		t.Fatalf("resolveCleanupDirs() returned %d entries, want %d", len(dirs), len(expected))
	}
	for _, dir := range dirs {
		if !expected[dir.Path] {
			t.Fatalf("unexpected cleanup path %q", dir.Path)
		}
	}
}

// TestCleanupDirsNeverTargetConfigRoot guards against cleanup deleting
// user-edited overrides: no per-project or --all cleanup target may resolve to
// a path under the XDG config root (~/.config/enclave).
func TestCleanupDirsNeverTargetConfigRoot(t *testing.T) {
	t.Parallel()

	home := "/tmp/test-home"
	project := model.Project{Hash: "projhash"}
	run := model.RunOptions{Tool: "codex"}
	configRoot := config.HostConfigRootDir(home)

	underConfigRoot := func(p string) bool {
		return p == configRoot || strings.HasPrefix(p, configRoot+string(filepath.Separator))
	}

	check := func(name string, dirs []cleanupDir) {
		for _, dir := range dirs {
			if underConfigRoot(dir.Path) {
				t.Fatalf("%s cleanup targets config-root path %q", name, dir.Path)
			}
		}
	}

	check("per-project", cleanupDirsForRemoval(run, model.CleanupOptions{}, home, project))
	check("all", cleanupDirsForRemoval(run, model.CleanupOptions{CleanupAll: true}, home, project))
}

func TestFilterDirsDropsMemory(t *testing.T) {
	t.Parallel()

	home := "/tmp/test-home"
	project := model.Project{Hash: "projhash"}
	run := model.RunOptions{Tool: "codex"}
	dirs := resolveCleanupDirs(run, model.CleanupOptions{}, home, project)

	memoryPath := config.HostProjectMemoryDir(home, project.Hash, run.Tool)
	filtered := filterDirs(dirs, "memory")

	if len(filtered) != len(dirs)-1 {
		t.Fatalf("filterDirs(dirs, \"memory\") returned %d entries, want %d", len(filtered), len(dirs)-1)
	}
	for _, dir := range filtered {
		if dir.Kind == "memory" {
			t.Fatalf("filterDirs did not drop memory entry: %q", dir.Path)
		}
		if dir.Path == memoryPath {
			t.Fatalf("filterDirs did not drop memory path: %q", dir.Path)
		}
	}
}

func TestCleanupDirGatingForMemory(t *testing.T) {
	t.Parallel()

	home := "/tmp/test-home"
	project := model.Project{Hash: "projhash"}
	run := model.RunOptions{Tool: "codex"}

	hasKind := func(dirs []cleanupDir, kind string) bool {
		for _, dir := range dirs {
			if dir.Kind == kind {
				return true
			}
		}
		return false
	}

	tests := []struct {
		name       string
		cleanup    model.CleanupOptions
		wantMemory bool
		wantAny    bool
	}{
		{name: "default removes memory", cleanup: model.CleanupOptions{}, wantMemory: true, wantAny: true},
		{name: "keep-memory preserves memory", cleanup: model.CleanupOptions{CleanupKeepMemory: true}, wantMemory: false, wantAny: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dirs := cleanupDirsForRemoval(run, tt.cleanup, home, project)
			if got := hasKind(dirs, "memory"); got != tt.wantMemory {
				t.Fatalf("memory present = %v, want %v", got, tt.wantMemory)
			}
			if tt.wantAny && len(dirs) == 0 {
				t.Fatal("expected host dirs in plan, got none")
			}
			if !tt.wantAny && len(dirs) != 0 {
				t.Fatalf("expected no host dirs in plan, got %d", len(dirs))
			}
		})
	}
}

func TestResolveEphemeralStoreDirsSkipsDefaultKey(t *testing.T) {
	home := t.TempDir()
	project := model.Project{Hash: "projhash1234"}
	run := model.RunOptions{Tool: "codex"}

	storeRoot := config.HostStoreConfigRootDir(home, run.Tool, project.Hash)
	for _, key := range []string{"default", "session-a", "session-b"} {
		if err := os.MkdirAll(filepath.Join(storeRoot, key), 0o700); err != nil {
			t.Fatalf("mkdir %q: %v", key, err)
		}
	}

	dirs := resolveEphemeralStoreDirs(run, model.CleanupOptions{}, home, project)
	if len(dirs) != 2 {
		t.Fatalf("resolveEphemeralStoreDirs() returned %d entries, want 2", len(dirs))
	}
	want := map[string]bool{
		filepath.Join(storeRoot, "session-a"): true,
		filepath.Join(storeRoot, "session-b"): true,
	}
	for _, dir := range dirs {
		if dir.Kind != "ephemeral" {
			t.Fatalf("unexpected kind %q for %q", dir.Kind, dir.Path)
		}
		if !want[dir.Path] {
			t.Fatalf("unexpected ephemeral store dir %q", dir.Path)
		}
	}
}

func TestResolveCleanupDirsAllIncludesAuthStores(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(config.HostStoreAuthDir(home, "codex", ""), 0o700); err != nil {
		t.Fatalf("mkdir auth: %v", err)
	}
	if err := os.MkdirAll(config.HostStoreFeatureAuthDir(home, "github-cli"), 0o700); err != nil {
		t.Fatalf("mkdir feature auth: %v", err)
	}

	all := model.CleanupOptions{CleanupAll: true}
	run := model.RunOptions{Tool: "codex"}

	hasAuth := func(dirs []cleanupDir) bool {
		for _, dir := range dirs {
			if dir.Kind == "auth" {
				return true
			}
		}
		return false
	}

	if !hasAuth(resolveCleanupDirs(run, all, home, model.Project{})) {
		t.Fatal("expected auth store dirs in full cleanup plan")
	}
	if !hasAuth(cleanupDirsForRemoval(run, all, home, model.Project{})) {
		t.Fatal("expected auth store dirs to be removed by default full cleanup")
	}
	keepAuth := model.CleanupOptions{CleanupAll: true, CleanupKeepAuth: true}
	if hasAuth(cleanupDirsForRemoval(run, keepAuth, home, model.Project{})) {
		t.Fatal("--keep-auth should preserve auth store dirs")
	}
}
