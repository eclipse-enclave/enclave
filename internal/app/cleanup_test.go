// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

	check("per-project", cleanupDirsForRemoval(run, model.CleanupOptions{}, home, project, model.MemoryScopeProject))
	check("all", cleanupDirsForRemoval(run, model.CleanupOptions{CleanupAll: true}, home, project, model.MemoryScopeProject))
}

func TestFilterDirsDropsMemory(t *testing.T) {
	t.Parallel()

	home := "/tmp/test-home"
	project := model.Project{Hash: "projhash"}
	run := model.RunOptions{Tool: "codex"}
	dirs := resolveCleanupDirs(run, model.CleanupOptions{}, home, project)

	memoryPath := config.HostProjectMemoryDir(home, project.Hash, run.Tool)
	filtered := filterDirs(dirs, memoryKind)

	if len(filtered) != len(dirs)-1 {
		t.Fatalf("filterDirs(dirs, memoryKind) returned %d entries, want %d", len(filtered), len(dirs)-1)
	}
	for _, dir := range filtered {
		if dir.Kind == memoryKind {
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
			dirs := cleanupDirsForRemoval(run, tt.cleanup, home, project, model.MemoryScopeProject)
			if got := hasKind(dirs, memoryKind); got != tt.wantMemory {
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

func TestCleanupKeepsSessionMemoryAndConfigTogether(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		scope      string
		cleanup    model.CleanupOptions
		wantMemory bool
		wantStore  bool
	}{
		{name: "project/default", scope: model.MemoryScopeProject},
		{name: "project/keep memory", scope: model.MemoryScopeProject,
			cleanup: model.CleanupOptions{CleanupKeepMemory: true}, wantMemory: true},
		{name: "project/keep history", scope: model.MemoryScopeProject,
			cleanup: model.CleanupOptions{CleanupKeepHist: true}, wantStore: true},
		{name: "project/keep both", scope: model.MemoryScopeProject,
			cleanup: model.CleanupOptions{CleanupKeepHist: true, CleanupKeepMemory: true}, wantMemory: true, wantStore: true},
		{name: "project/all ignores keep memory", scope: model.MemoryScopeProject,
			cleanup: model.CleanupOptions{CleanupAll: true, CleanupKeepMemory: true}},
		{name: "session/default", scope: model.MemoryScopeSession},
		{name: "session/keep memory keeps store", scope: model.MemoryScopeSession,
			cleanup: model.CleanupOptions{CleanupKeepMemory: true}, wantMemory: true, wantStore: true},
		{name: "session/keep history keeps memory", scope: model.MemoryScopeSession,
			cleanup: model.CleanupOptions{CleanupKeepHist: true}, wantMemory: true, wantStore: true},
		{name: "session/keep both", scope: model.MemoryScopeSession,
			cleanup: model.CleanupOptions{CleanupKeepHist: true, CleanupKeepMemory: true}, wantMemory: true, wantStore: true},
		{name: "session/all ignores keep memory", scope: model.MemoryScopeSession,
			cleanup: model.CleanupOptions{CleanupAll: true, CleanupKeepMemory: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			project := model.Project{Hash: "project"}
			run := model.RunOptions{Tool: "custom"}
			memory := config.HostProjectMemoryDir(home, project.Hash, run.Tool)
			store := config.HostStoreConfigRootDir(home, run.Tool, project.Hash)
			for _, root := range []string{memory, store} {
				if err := os.MkdirAll(filepath.Join(root, "default"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			dirs := cleanupDirsForRemoval(run, tc.cleanup, home, project, tc.scope)
			cleanupDirs(dirs)
			for root, want := range map[string]bool{memory: tc.wantMemory, store: tc.wantStore} {
				_, err := os.Stat(root)
				if (err == nil) != want {
					t.Errorf("%s exists = %v, want %v", root, err == nil, want)
				}
			}
		})
	}
}

// writeScopedToolSpec writes a minimal sandbox spec declaring memoryScope into
// a temporary user-tools tree, so cleanup tests exercise the scope branches
// without depending on what the bundled extensions happen to declare.
func writeScopedToolSpec(t *testing.T, tool string, scope string) model.Paths {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, tool), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := fmt.Sprintf(`{"schemaVersion":"1","kind":"sandbox","name":%q,"sandbox":{
		"configDir":".tool", "memoryDir":".tool/memories", "memoryScope":%q}}`, tool, scope)
	if err := os.WriteFile(filepath.Join(root, tool, config.SpecFilenameJSON), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	return model.Paths{UserToolsDir: root}
}

// TestResolveEphemeralStoreDirs covers the ephemeral plan for a session-scoped
// tool: the persistent key is never touched, a key with session memory is
// planned as a store/memory pair, and a key without one (an --ephemeral run's
// throwaway store, which never gets a memory mount) is planned alone.
func TestResolveEphemeralStoreDirs(t *testing.T) {
	home := t.TempDir()
	project := model.Project{Hash: "projhash1234"}
	run := model.RunOptions{Tool: "custom"}

	storeRoot := config.HostStoreConfigRootDir(home, run.Tool, project.Hash)
	memoryRoot := config.HostProjectMemoryDir(home, project.Hash, run.Tool)
	for _, key := range []string{"default", "session-a", "throwaway"} {
		if err := os.MkdirAll(filepath.Join(storeRoot, key), 0o700); err != nil {
			t.Fatalf("mkdir store %q: %v", key, err)
		}
	}
	for _, key := range []string{"default", "session-a"} {
		if err := os.MkdirAll(filepath.Join(memoryRoot, key), 0o700); err != nil {
			t.Fatalf("mkdir memory %q: %v", key, err)
		}
	}

	paths := writeScopedToolSpec(t, run.Tool, model.MemoryScopeSession)
	for _, tc := range []struct {
		name    string
		cleanup model.CleanupOptions
		want    []cleanupDir
	}{
		// Entries are sorted by path, and config-store/ sorts before memory/.
		{
			name: "default",
			want: []cleanupDir{
				{Kind: ephemeralKind, Path: filepath.Join(storeRoot, "session-a")},
				{Kind: ephemeralKind, Path: filepath.Join(storeRoot, "throwaway")},
				{Kind: memoryKind, Path: filepath.Join(memoryRoot, "session-a")},
			},
		},
		{
			// Keeping memory keeps the config store indexing it, but the
			// throwaway store has no memory to keep and still goes.
			name:    "keep memory",
			cleanup: model.CleanupOptions{CleanupKeepMemory: true},
			want:    []cleanupDir{{Kind: ephemeralKind, Path: filepath.Join(storeRoot, "throwaway")}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dirs, err := resolveEphemeralStoreDirs(run, tc.cleanup, home, project, newMemoryScopeResolver(paths, nil))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(dirs, tc.want) {
				t.Fatalf("ephemeral dirs = %+v, want %+v", dirs, tc.want)
			}
		})
	}
}

// TestCleanupToleratesUninstalledTools covers state left behind by a tool that
// has since been removed from the extension tree: its directories still carry
// ephemeral stores, and cleanup must plan their removal instead of failing on
// the missing spec.
func TestCleanupToleratesUninstalledTools(t *testing.T) {
	home := t.TempDir()
	project := model.Project{Hash: "projhash1234"}
	run := model.RunOptions{Tool: "gone"}

	storeRoot := config.HostStoreConfigRootDir(home, run.Tool, project.Hash)
	if err := os.MkdirAll(filepath.Join(storeRoot, "session-a"), 0o700); err != nil {
		t.Fatal(err)
	}

	scopes := newMemoryScopeResolver(model.Paths{UserToolsDir: t.TempDir()}, nil)
	dirs, err := resolveEphemeralStoreDirs(run, model.CleanupOptions{}, home, project, scopes)
	if err != nil {
		t.Fatalf("resolveEphemeralStoreDirs() with no spec: %v", err)
	}
	want := []cleanupDir{{Kind: ephemeralKind, Path: filepath.Join(storeRoot, "session-a")}}
	if !reflect.DeepEqual(dirs, want) {
		t.Fatalf("ephemeral dirs = %+v, want %+v", dirs, want)
	}

	scope, err := scopes.scopeFor(run.Tool)
	if err != nil {
		t.Fatalf("scopeFor() with no spec: %v", err)
	}
	if scope != model.MemoryScopeProject {
		t.Fatalf("scope = %q, want %q", scope, model.MemoryScopeProject)
	}
}

// TestScopeForResolvesUndeclaredScope pins that a spec declaring no memoryScope
// answers the same as no spec at all. The two arrive by different routes, one
// through profile normalization and the other through the resolver's own
// fallback, and a caller comparing against model.MemoryScopeProject must not be
// able to tell them apart.
func TestScopeForResolvesUndeclaredScope(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plain"), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := `{"schemaVersion":"1","kind":"sandbox","name":"plain","sandbox":{"configDir":".plain"}}`
	if err := os.WriteFile(filepath.Join(root, "plain", config.SpecFilenameJSON), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}

	declared, err := newMemoryScopeResolver(model.Paths{UserToolsDir: root}, nil).scopeFor("plain")
	if err != nil {
		t.Fatal(err)
	}
	absent, err := newMemoryScopeResolver(model.Paths{UserToolsDir: t.TempDir()}, nil).scopeFor("plain")
	if err != nil {
		t.Fatal(err)
	}
	if declared != model.MemoryScopeProject || absent != model.MemoryScopeProject {
		t.Fatalf("scope without memoryScope = %q, scope without spec = %q, want %q for both",
			declared, absent, model.MemoryScopeProject)
	}
}

// TestUnloadableSpecOnlyBlocksScopeSensitivePlans covers the extension the user
// is most likely cleaning up after: one whose spec this binary cannot load.
// Refusing to plan is right only when the scope could still change what is
// removed; otherwise cleanup would strand the very state it exists to delete.
func TestUnloadableSpecOnlyBlocksScopeSensitivePlans(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := `{"schemaVersion":"1","kind":"sandbox","name":"broken","sandbox":{
		"configDir":".broken", "memoryScope":"global"}}`
	if err := os.WriteFile(filepath.Join(root, "broken", config.SpecFilenameJSON), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newMemoryScopeResolver(model.Paths{UserToolsDir: root}, nil).scopeFor("broken"); err == nil {
		t.Fatal("expected an unloadable spec to report an error")
	}

	for _, tc := range []struct {
		name       string
		cleanup    model.CleanupOptions
		wantAborts bool
	}{
		{name: "default"},
		{name: "all", cleanup: model.CleanupOptions{CleanupAll: true}},
		{name: "all keep auth", cleanup: model.CleanupOptions{CleanupAll: true, CleanupKeepAuth: true}},
		{name: "all keep memory", cleanup: model.CleanupOptions{CleanupAll: true, CleanupKeepMemory: true}},
		{name: "keep cache", cleanup: model.CleanupOptions{CleanupKeepCache: true}},
		{name: "keep memory", cleanup: model.CleanupOptions{CleanupKeepMemory: true}, wantAborts: true},
		{name: "keep history", cleanup: model.CleanupOptions{CleanupKeepHist: true}, wantAborts: true},
		{name: "ephemeral", cleanup: model.CleanupOptions{CleanupEphemeral: true}, wantAborts: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := memoryScopeAffectsPlan(tc.cleanup); got != tc.wantAborts {
				t.Fatalf("memoryScopeAffectsPlan() = %v, want %v", got, tc.wantAborts)
			}
			if tc.wantAborts {
				return
			}
			// The plan the run falls back to must be the one the default scope
			// produces, so continuing past the error cannot delete more or less
			// than a working spec would have.
			home := t.TempDir()
			project := model.Project{Hash: "projhash1234"}
			run := model.RunOptions{Tool: "broken"}
			fallback := cleanupDirsForRemoval(run, tc.cleanup, home, project, model.MemoryScopeProject)
			for _, scope := range []string{model.MemoryScopeProject, model.MemoryScopeSession} {
				dirs := cleanupDirsForRemoval(run, tc.cleanup, home, project, scope)
				if !reflect.DeepEqual(dirs, fallback) {
					t.Fatalf("scope %q changes a plan it should not: %+v, want %+v", scope, dirs, fallback)
				}
			}
		})
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
			if dir.Kind == authKind {
				return true
			}
		}
		return false
	}

	if !hasAuth(resolveCleanupDirs(run, all, home, model.Project{})) {
		t.Fatal("expected auth store dirs in full cleanup plan")
	}
	if !hasAuth(cleanupDirsForRemoval(run, all, home, model.Project{}, model.MemoryScopeProject)) {
		t.Fatal("expected auth store dirs to be removed by default full cleanup")
	}
	keepAuth := model.CleanupOptions{CleanupAll: true, CleanupKeepAuth: true}
	if hasAuth(cleanupDirsForRemoval(run, keepAuth, home, model.Project{}, model.MemoryScopeProject)) {
		t.Fatal("--keep-auth should preserve auth store dirs")
	}
}
