// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/util"
)

func newFSStore(t *testing.T) StoreManager {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return StoreManager{host: model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}}
}

func TestStoreManagerWriteReadRoundTripCreatesDirsAndAppliesMode(t *testing.T) {
	store := newFSStore(t)
	key := backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123"}

	if err := store.WriteFile(context.Background(), key, backend.StoreKindConfig, "agent/settings.json", []byte(`{"a":1}`), 0o640); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := store.ReadFile(context.Background(), key, backend.StoreKindConfig, "agent/settings.json")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("ReadFile() = %q, want %q", got, `{"a":1}`)
	}

	target := filepath.Join(config.HostStoreConfigDir(store.host.Home, key.Owner, key.ProjectHash, "default"), "agent", "settings.json")
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat store file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("store file perm = %o, want 640", perm)
	}
}

func TestStoreManagerWriteFileDefaultsToMode0600(t *testing.T) {
	store := newFSStore(t)
	key := backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123"}

	if err := store.WriteFile(context.Background(), key, backend.StoreKindEnv, "env", []byte("K=V"), 0); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	target := filepath.Join(config.HostStoreEnvDir(store.host.Home, key.Owner, key.ProjectHash), "env")
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat store file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("store file perm = %o, want 600", perm)
	}
}

func TestStoreManagerRejectsPathTraversal(t *testing.T) {
	store := newFSStore(t)
	key := backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123"}

	if err := store.WriteFile(context.Background(), key, backend.StoreKindConfig, "../escape", []byte("x"), 0o600); err == nil {
		t.Fatalf("WriteFile() with traversal path returned nil error")
	}
	if _, err := store.ReadFile(context.Background(), key, backend.StoreKindConfig, "../../escape"); err == nil {
		t.Fatalf("ReadFile() with traversal path returned nil error")
	}
	if err := store.RemovePath(context.Background(), key, backend.StoreKindConfig, "../escape"); err == nil {
		t.Fatalf("RemovePath() with traversal path returned nil error")
	}
}

func TestStoreManagerReadFileMissingReportsNotExist(t *testing.T) {
	store := newFSStore(t)
	key := backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123"}

	_, err := store.ReadFile(context.Background(), key, backend.StoreKindConfig, "missing")
	if !os.IsNotExist(err) {
		t.Fatalf("ReadFile() error = %v, want not-exist", err)
	}
}

func TestStoreManagerSeedCopiesFilesAndDirsAndAppliesMode(t *testing.T) {
	store := newFSStore(t)
	key := backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123"}

	srcRoot := t.TempDir()
	fileSrc := filepath.Join(srcRoot, "settings.json")
	if err := os.WriteFile(fileSrc, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	dirSrc := filepath.Join(srcRoot, "tree")
	if err := os.MkdirAll(filepath.Join(dirSrc, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir seed tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirSrc, "nested", "a.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatalf("write seed tree file: %v", err)
	}

	if err := store.Seed(context.Background(), key, backend.StoreKindConfig, []backend.SeedItem{
		{HostPath: fileSrc, StoreRel: "agent/settings.json", Mode: 0o644},
		{HostPath: dirSrc, StoreRel: "agent/tree"},
	}); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}

	base := config.HostStoreConfigDir(store.host.Home, key.Owner, key.ProjectHash, "default")

	info, err := os.Stat(filepath.Join(base, "agent", "settings.json"))
	if err != nil {
		t.Fatalf("stat seeded file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("seeded file perm = %o, want 644", perm)
	}

	nested, err := os.ReadFile(filepath.Join(base, "agent", "tree", "nested", "a.txt"))
	if err != nil {
		t.Fatalf("read seeded tree file: %v", err)
	}
	if string(nested) != "hi" {
		t.Fatalf("seeded tree file = %q, want %q", nested, "hi")
	}
}

func TestStoreManagerRemovePathAndRemove(t *testing.T) {
	store := newFSStore(t)
	key := backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123"}

	if err := store.WriteFile(context.Background(), key, backend.StoreKindConfig, "agent/settings.json", []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := store.RemovePath(context.Background(), key, backend.StoreKindConfig, "agent/settings.json"); err != nil {
		t.Fatalf("RemovePath() error = %v", err)
	}
	if _, err := store.ReadFile(context.Background(), key, backend.StoreKindConfig, "agent/settings.json"); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, got err = %v", err)
	}

	if err := store.Remove(context.Background(), key, backend.StoreKindConfig); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(config.HostStoreConfigDir(store.host.Home, key.Owner, key.ProjectHash, "default")); !os.IsNotExist(err) {
		t.Fatalf("expected store dir removed, got err = %v", err)
	}
}

func TestStoreManagerEnsureAndStoreExists(t *testing.T) {
	store := newFSStore(t)
	key := backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123"}

	exists, err := store.StoreExists(context.Background(), key, backend.StoreKindConfig)
	if err != nil {
		t.Fatalf("StoreExists() error = %v", err)
	}
	if exists {
		t.Fatalf("StoreExists() = true before Ensure")
	}

	if err := store.Ensure(context.Background(), key, backend.StoreKindConfig, "1000:1000"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	exists, err = store.StoreExists(context.Background(), key, backend.StoreKindConfig)
	if err != nil {
		t.Fatalf("StoreExists() error = %v", err)
	}
	if !exists {
		t.Fatalf("StoreExists() = false after Ensure")
	}
}

func TestStoreManagerRejectsMaliciousKeyOnWrite(t *testing.T) {
	store := newFSStore(t)
	key := backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123", Suffix: "../escape"}
	if err := store.WriteFile(context.Background(), key, backend.StoreKindConfig, "env", []byte("x"), 0o600); err == nil {
		t.Fatalf("WriteFile() with malicious suffix returned nil error")
	}
}

func TestStoreManagerRejectsSymlinkedParent(t *testing.T) {
	store := newFSStore(t)
	key := backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123"}

	base := config.HostStoreConfigDir(store.host.Home, key.Owner, key.ProjectHash, "default")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatalf("mkdir store dir: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "link")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	if err := store.WriteFile(context.Background(), key, backend.StoreKindConfig, "link/pwned", []byte("x"), 0o600); err == nil {
		t.Fatalf("WriteFile() through symlinked parent returned nil error")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned")); err == nil {
		t.Fatalf("write escaped the store via symlink")
	}
	if _, err := store.ReadFile(context.Background(), key, backend.StoreKindConfig, "link/anything"); err == nil {
		t.Fatalf("ReadFile() through symlinked parent returned nil error")
	}
}

func TestStoreManagerRejectsSymlinkedLeafOnReadAndSeed(t *testing.T) {
	store := newFSStore(t)
	key := backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123"}

	base := config.HostStoreConfigDir(store.host.Home, key.Owner, key.ProjectHash, "default")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatalf("mkdir store dir: %v", err)
	}

	outsideFile := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(base, "readlink")); err != nil {
		t.Fatalf("create read symlink: %v", err)
	}
	if _, err := store.ReadFile(context.Background(), key, backend.StoreKindConfig, "readlink"); err == nil {
		t.Fatalf("ReadFile() through symlinked leaf returned nil error")
	}

	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(base, "seedlink")); err != nil {
		t.Fatalf("create seed symlink: %v", err)
	}
	seedSrc := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(seedSrc, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write seed source: %v", err)
	}
	if err := store.Seed(context.Background(), key, backend.StoreKindConfig, []backend.SeedItem{
		{HostPath: seedSrc, StoreRel: "seedlink/pwned", Mode: 0o644},
	}); err == nil {
		t.Fatalf("Seed() through symlinked leaf parent returned nil error")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "pwned")); err == nil {
		t.Fatalf("seed escaped the store via symlinked directory")
	}

	// A store-relative leaf that is itself a symlink to an outside directory
	// must be rejected by Seed rather than followed.
	if err := store.Seed(context.Background(), key, backend.StoreKindConfig, []backend.SeedItem{
		{HostPath: seedSrc, StoreRel: "seedlink", Mode: 0o644},
	}); err == nil {
		t.Fatalf("Seed() onto symlinked leaf returned nil error")
	}
}

func TestStoreManagerRejectsSymlinkedStoreRoot(t *testing.T) {
	store := newFSStore(t)
	key := backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123"}

	base := config.HostStoreConfigDir(store.host.Home, key.Owner, key.ProjectHash, "default")
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		t.Fatalf("mkdir store parent: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, base); err != nil {
		t.Fatalf("plant symlinked store root: %v", err)
	}

	if err := store.WriteFile(context.Background(), key, backend.StoreKindConfig, "pwned", []byte("x"), 0o600); err == nil {
		t.Fatalf("WriteFile() through symlinked store root returned nil error")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned")); err == nil {
		t.Fatalf("write escaped the store via symlinked store root")
	}
	if _, err := store.ReadFile(context.Background(), key, backend.StoreKindConfig, "anything"); err == nil {
		t.Fatalf("ReadFile() through symlinked store root returned nil error")
	}

	seedSrc := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(seedSrc, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write seed source: %v", err)
	}
	if err := store.Seed(context.Background(), key, backend.StoreKindConfig, []backend.SeedItem{
		{HostPath: seedSrc, StoreRel: "seeded", Mode: 0o644},
	}); err == nil {
		t.Fatalf("Seed() through symlinked store root returned nil error")
	}
	if _, err := os.Stat(filepath.Join(outside, "seeded")); err == nil {
		t.Fatalf("seed escaped the store via symlinked store root")
	}
	if err := store.RemovePath(context.Background(), key, backend.StoreKindConfig, "anything"); err == nil {
		t.Fatalf("RemovePath() through symlinked store root returned nil error")
	}
}

func TestStoreManagerSeedRejectsNestedSymlinkInDestinationTree(t *testing.T) {
	store := newFSStore(t)
	key := backend.StoreKey{Owner: "codex", ProjectHash: "abc123abc123"}

	base := config.HostStoreConfigDir(store.host.Home, key.Owner, key.ProjectHash, "default")

	// Pre-existing destination tree with a planted symlinked subdirectory and a
	// planted symlinked file leaf.
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(t.TempDir(), "target-file")
	if err := os.WriteFile(outsideFile, []byte("orig"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(base, "tree"), 0o700); err != nil {
		t.Fatalf("mkdir destination tree: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(base, "tree", "nested")); err != nil {
		t.Fatalf("plant symlinked subdir: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(base, "tree", "file.txt")); err != nil {
		t.Fatalf("plant symlinked file leaf: %v", err)
	}

	// Source tree that would write into both the symlinked subdir and the
	// symlinked file leaf if the copy followed them.
	srcRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcRoot, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir source tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "nested", "pwned"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write source nested file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "file.txt"), []byte("overwrite"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	if err := store.Seed(context.Background(), key, backend.StoreKindConfig, []backend.SeedItem{
		{HostPath: srcRoot, StoreRel: "tree", Mode: 0o644},
	}); err == nil {
		t.Fatalf("Seed() into destination tree with nested symlink returned nil error")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "pwned")); err == nil {
		t.Fatalf("seed escaped the store via nested symlinked subdirectory")
	}
	if data, err := os.ReadFile(outsideFile); err != nil || string(data) != "orig" {
		t.Fatalf("seed followed symlinked file leaf: data=%q err=%v", data, err)
	}
}

func installFakeDocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.args")
	fakeDocker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> " + util.ShellQuote(logPath) + "; done\n" +
		"printf '%s\\n' -- >> " + util.ShellQuote(logPath) + "\n" +
		"if [ \"$1\" = \"run\" ]; then printf '%s\\n' fake-container-id; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func readFakeDockerArgs(t *testing.T, logPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake docker args: %v", err)
	}
	var args []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" && line != "--" {
			args = append(args, line)
		}
	}
	return args
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
