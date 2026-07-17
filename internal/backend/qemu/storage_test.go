// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package qemu

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

func TestStoreManagerReadWriteRemove(t *testing.T) {
	store := newStoreManager(model.Host{Home: t.TempDir()})
	key := backend.StoreKey{Owner: "tool", ProjectHash: "abc123def456"}
	ctx := context.Background()

	if err := store.WriteFile(ctx, key, backend.StoreKindEnv, "nested/env", []byte("A=B\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := store.ReadFile(ctx, key, backend.StoreKindEnv, "nested/env")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "A=B\n" {
		t.Fatalf("data = %q", data)
	}
	if err := store.RemovePath(ctx, key, backend.StoreKindEnv, "nested/env"); err != nil {
		t.Fatalf("RemovePath: %v", err)
	}
	if _, err := store.ReadFile(ctx, key, backend.StoreKindEnv, "nested/env"); !os.IsNotExist(err) {
		t.Fatalf("ReadFile after remove err = %v, want not exist", err)
	}
}

// TestMountSourceResolvesSharedStoreDirs pins qemu store mounts to the shared
// host-store layout, so qemu guests keep reading and writing the exact same
// auth/config/env directories as Docker sessions of the same tool/project.
func TestMountSourceResolvesSharedStoreDirs(t *testing.T) {
	home := t.TempDir()
	store := newStoreManager(model.Host{Home: home})

	authDir, err := store.MountSource(backend.StoreKey{Owner: "claude"}, backend.StoreKindAuth)
	if err != nil {
		t.Fatalf("MountSource(auth): %v", err)
	}
	if want := config.HostStoreAuthDir(home, "claude", ""); authDir != want {
		t.Fatalf("auth mount source = %q, want shared store dir %q", authDir, want)
	}

	configDir, err := store.MountSource(backend.StoreKey{Owner: "claude", ProjectHash: "abc123def456"}, backend.StoreKindConfig)
	if err != nil {
		t.Fatalf("MountSource(config): %v", err)
	}
	if want := config.HostStoreConfigDir(home, "claude", "abc123def456", "default"); configDir != want {
		t.Fatalf("config mount source = %q, want shared store dir %q", configDir, want)
	}
}

func TestWithStoreLockUsesSharedPerStoreLockFiles(t *testing.T) {
	home := t.TempDir()
	store := newStoreManager(model.Host{Home: home})
	key := backend.StoreKey{Owner: "tool"}
	ctx := context.Background()

	for _, kind := range []backend.StoreKind{backend.StoreKindAuth, backend.StoreKindFeatureAuth} {
		if err := store.WithStoreLock(ctx, key, kind, func() error { return nil }); err != nil {
			t.Fatalf("WithStoreLock(%s): %v", kind, err)
		}
	}

	// Locks derive from the shared store directories (hoststore.WithLock), so
	// they land in the host locks dir shared with the Docker backend, one per
	// store.
	entries, err := os.ReadDir(config.HostLocksDir(home))
	if err != nil {
		t.Fatalf("read locks dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("locks dir entries = %d, want one per store kind", len(entries))
	}
}

// TestEnsureToleratesForeignOwnedStoreEntries covers the shared-store case
// where another backend leaves entries the invoking user cannot chown (e.g.
// dockerd creates root-owned bind-mount point directories like
// config-store/default/memory). Ensure must not attempt ownership changes.
func TestEnsureToleratesForeignOwnedStoreEntries(t *testing.T) {
	store := newStoreManager(model.Host{Home: t.TempDir()})
	key := backend.StoreKey{Owner: "tool", ProjectHash: "abc123def456"}
	ctx := context.Background()

	root, err := store.MountSource(key, backend.StoreKindConfig)
	if err != nil {
		t.Fatalf("mount source: %v", err)
	}
	// A mode-0 directory stands in for a foreign-owned mount point: any
	// chown/walk attempt inside it would fail, while plain MkdirAll of the
	// store root succeeds.
	mountPoint := filepath.Join(root, "memory")
	if err := os.Mkdir(mountPoint, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mountPoint, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(mountPoint, 0o700) })

	if err := store.Ensure(ctx, key, backend.StoreKindConfig, "1000:1000"); err != nil {
		t.Fatalf("Ensure() over store with foreign-owned entry: %v", err)
	}
}

func TestPrepareStoresConfigOverlayPreservesAuthFiles(t *testing.T) {
	be := New(Options{Host: model.Host{Home: t.TempDir()}})
	key := backend.StoreKey{Owner: "tool", ProjectHash: "abc123def456"}
	ctx := context.Background()

	if err := be.storage.WriteFile(ctx, key, backend.StoreKindConfig, "auth/session.json", []byte("old"), 0o600); err != nil {
		t.Fatalf("seed auth: %v", err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("new"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	_, err := be.PrepareStores(ctx, backend.StorePrep{Config: &backend.ConfigStorePrep{
		Key: key,
		Overlay: &backend.ConfigOverlaySpec{
			SourceDir:     source,
			PreservePaths: []string{"auth/session.json"},
		},
	}})
	if err != nil {
		t.Fatalf("PrepareStores: %v", err)
	}
	preserved, err := be.storage.ReadFile(ctx, key, backend.StoreKindConfig, "auth/session.json")
	if err != nil {
		t.Fatalf("read preserved: %v", err)
	}
	if string(preserved) != "old" {
		t.Fatalf("preserved = %q, want old", preserved)
	}
	settings, err := be.storage.ReadFile(ctx, key, backend.StoreKindConfig, "settings.json")
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if string(settings) != "new" {
		t.Fatalf("settings = %q, want new", settings)
	}
}

func TestPrepareStoresConfigOverlayDoesNotFollowSymlinks(t *testing.T) {
	be := New(Options{Host: model.Host{Home: t.TempDir()}})
	key := backend.StoreKey{Owner: "tool", ProjectHash: "abc123def456"}
	ctx := context.Background()

	secretPath := filepath.Join(t.TempDir(), "host-secret")
	if err := os.WriteFile(secretPath, []byte("HOST-SECRET"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	root, err := be.storage.MountSource(key, backend.StoreKindConfig)
	if err != nil {
		t.Fatalf("mount source: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0o700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	link := filepath.Join(root, "sessions", "leak")
	if err := os.Symlink(secretPath, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("new"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if _, err := be.PrepareStores(ctx, backend.StorePrep{Config: &backend.ConfigStorePrep{
		Key: key,
		Overlay: &backend.ConfigOverlaySpec{
			SourceDir:     source,
			PreservePaths: []string{"sessions/"},
		},
	}}); err != nil {
		t.Fatalf("PrepareStores: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("preserved symlink missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("preserved entry was materialized as a regular file; the guest symlink was followed")
	}
}

func TestPrepareStoresConfigOverlayPreservesGlobPaths(t *testing.T) {
	be := New(Options{Host: model.Host{Home: t.TempDir()}})
	key := backend.StoreKey{Owner: "tool", ProjectHash: "abc123def456"}
	ctx := context.Background()

	if err := be.storage.WriteFile(ctx, key, backend.StoreKindConfig, "shell.history", []byte("hist"), 0o600); err != nil {
		t.Fatalf("seed history: %v", err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("new"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if _, err := be.PrepareStores(ctx, backend.StorePrep{Config: &backend.ConfigStorePrep{
		Key: key,
		Overlay: &backend.ConfigOverlaySpec{
			SourceDir:     source,
			PreservePaths: []string{"*.history"},
		},
	}}); err != nil {
		t.Fatalf("PrepareStores: %v", err)
	}
	got, err := be.storage.ReadFile(ctx, key, backend.StoreKindConfig, "shell.history")
	if err != nil {
		t.Fatalf("glob-preserved file missing: %v", err)
	}
	if string(got) != "hist" {
		t.Fatalf("preserved = %q, want hist", got)
	}
}

func TestStoreReadFileRejectsSymlinkedParent(t *testing.T) {
	be := New(Options{Host: model.Host{Home: t.TempDir()}})
	key := backend.StoreKey{Owner: "tool", ProjectHash: "abc123def456"}
	ctx := context.Background()

	secretDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(secretDir, "auth.json"), []byte("HOST-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := be.storage.MountSource(key, backend.StoreKindConfig)
	if err != nil {
		t.Fatalf("mount source: %v", err)
	}
	if err := os.Symlink(secretDir, filepath.Join(root, "agent")); err != nil {
		t.Fatalf("symlink parent: %v", err)
	}

	if _, err := be.storage.ReadFile(ctx, key, backend.StoreKindConfig, "agent/auth.json"); err == nil {
		t.Fatalf("ReadFile followed a guest-planted symlinked parent and disclosed a host file")
	}
}

func TestPrepareStoresConfigOverlayRejectsSymlinkedParent(t *testing.T) {
	be := New(Options{Host: model.Host{Home: t.TempDir()}})
	key := backend.StoreKey{Owner: "tool", ProjectHash: "abc123def456"}
	ctx := context.Background()

	secretDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(secretDir, "auth.json"), []byte("HOST-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := be.storage.MountSource(key, backend.StoreKindConfig)
	if err != nil {
		t.Fatalf("mount source: %v", err)
	}
	if err := os.Symlink(secretDir, filepath.Join(root, "agent")); err != nil {
		t.Fatalf("symlink parent: %v", err)
	}

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := be.PrepareStores(ctx, backend.StorePrep{Config: &backend.ConfigStorePrep{
		Key: key,
		Overlay: &backend.ConfigOverlaySpec{
			SourceDir:     source,
			PreservePaths: []string{"agent/auth.json"},
		},
	}}); err != nil {
		t.Fatalf("PrepareStores: %v", err)
	}

	if data, err := be.storage.ReadFile(ctx, key, backend.StoreKindConfig, "agent/auth.json"); err == nil && string(data) == "HOST-SECRET" {
		t.Fatalf("host secret exfiltrated into the store through a symlinked parent")
	}
}

func TestStoreSeedReplacesDestinationSymlink(t *testing.T) {
	store := newStoreManager(model.Host{Home: t.TempDir()})
	key := backend.StoreKey{Owner: "tool"}
	ctx := context.Background()

	victim := filepath.Join(t.TempDir(), "host-file")
	if err := os.WriteFile(victim, []byte("HOST"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := store.MountSource(key, backend.StoreKindAuth)
	if err != nil {
		t.Fatalf("mount source: %v", err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "auth.json")); err != nil {
		t.Fatalf("symlink destination: %v", err)
	}
	source := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(source, []byte("SAFE"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.Seed(ctx, key, backend.StoreKindAuth, []backend.SeedItem{{HostPath: source, StoreRel: "auth.json", Mode: 0o600}}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	victimData, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(victimData) != "HOST" {
		t.Fatalf("Seed followed destination symlink and overwrote host file: %q", victimData)
	}
	info, err := os.Lstat(filepath.Join(root, "auth.json"))
	if err != nil {
		t.Fatalf("lstat seeded path: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("seeded path is still a symlink")
	}
	got, err := store.ReadFile(ctx, key, backend.StoreKindAuth, "auth.json")
	if err != nil {
		t.Fatalf("read seeded path: %v", err)
	}
	if string(got) != "SAFE" {
		t.Fatalf("seeded data = %q, want SAFE", got)
	}
}

func TestStoreSeedReplacesDestinationSymlinkedDirectory(t *testing.T) {
	store := newStoreManager(model.Host{Home: t.TempDir()})
	key := backend.StoreKey{Owner: "tool", ProjectHash: "abc123def456"}
	ctx := context.Background()

	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "auth.json")
	if err := os.WriteFile(victim, []byte("HOST"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := store.MountSource(key, backend.StoreKindConfig)
	if err != nil {
		t.Fatalf("mount source: %v", err)
	}
	if err := os.Symlink(victimDir, filepath.Join(root, "agent")); err != nil {
		t.Fatalf("symlink destination directory: %v", err)
	}
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "auth.json"), []byte("SAFE"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.Seed(ctx, key, backend.StoreKindConfig, []backend.SeedItem{{HostPath: sourceDir, StoreRel: "agent", Mode: 0o600}}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	victimData, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(victimData) != "HOST" {
		t.Fatalf("Seed followed symlinked destination directory and overwrote host file: %q", victimData)
	}
	info, err := os.Lstat(filepath.Join(root, "agent"))
	if err != nil {
		t.Fatalf("lstat seeded directory: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("seeded directory = mode %v, want real directory", info.Mode())
	}
	got, err := store.ReadFile(ctx, key, backend.StoreKindConfig, "agent/auth.json")
	if err != nil {
		t.Fatalf("read seeded path: %v", err)
	}
	if string(got) != "SAFE" {
		t.Fatalf("seeded data = %q, want SAFE", got)
	}
}

func TestPrepareStoresLayoutDirReplacesSymlink(t *testing.T) {
	be := New(Options{Host: model.Host{Home: t.TempDir()}})
	key := backend.StoreKey{Owner: "tool", ProjectHash: "abc123def456"}
	ctx := context.Background()

	root, err := be.storage.MountSource(key, backend.StoreKindConfig)
	if err != nil {
		t.Fatalf("mount source: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "agent")); err != nil {
		t.Fatalf("symlink layout dir: %v", err)
	}

	if _, err := be.PrepareStores(ctx, backend.StorePrep{Config: &backend.ConfigStorePrep{Key: key, LayoutDirs: []string{"agent"}}}); err != nil {
		t.Fatalf("PrepareStores: %v", err)
	}
	info, err := os.Lstat(filepath.Join(root, "agent"))
	if err != nil {
		t.Fatalf("lstat layout dir: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("layout path = mode %v, want real directory", info.Mode())
	}
}
