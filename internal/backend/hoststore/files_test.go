// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hoststore

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"enclave/internal/backend"
)

func TestRootedOperationsRejectPathSwap(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	sentinel := filepath.Join(outside, "auth")
	if err := os.WriteFile(sentinel, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	// Swap the parent for an outside link after the path check has passed.
	if err := EnsureNoSymlinkChain(dir, filepath.Join(dir, "agent", "auth"), true); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "agent"), filepath.Join(dir, "saved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "agent")); err != nil {
		t.Fatal(err)
	}
	if data, err := root.ReadFile("agent/auth"); err == nil {
		t.Fatalf("read followed swapped parent: %q", data)
	}
	if err := root.WriteFile("agent/auth", []byte("overwrite"), 0o600); err == nil {
		t.Fatal("write followed swapped parent")
	}
	if err := root.RemoveAll("agent/auth"); err == nil {
		t.Fatal("removal followed swapped parent")
	}
	if err := root.MkdirAll("agent/projects", 0o700); err == nil {
		t.Fatal("directory creation followed swapped parent")
	}
	if _, err := os.Lstat(filepath.Join(outside, "projects")); !os.IsNotExist(err) {
		t.Fatalf("outside directory created: %v", err)
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "outside-secret" {
		t.Fatalf("sentinel changed: %q, %v", data, err)
	}
}

func TestRootedOperationsDuringConcurrentRename(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "auth"), []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = os.Rename(filepath.Join(dir, "agent"), filepath.Join(dir, "saved"))
			_ = os.Symlink(outside, filepath.Join(dir, "agent"))
			_ = os.Remove(filepath.Join(dir, "agent"))
			_ = os.Rename(filepath.Join(dir, "saved"), filepath.Join(dir, "agent"))
		}
	}()
	for i := 0; i < 500; i++ {
		_ = root.WriteFile("agent/auth", []byte("inside"), 0o600)
		if data, err := root.ReadFile("agent/auth"); err == nil && string(data) != "inside" {
			t.Errorf("read outside: %q", data)
		}
	}
	wg.Wait()
	data, err := os.ReadFile(filepath.Join(outside, "auth"))
	if err != nil || string(data) != "outside-secret" {
		t.Fatalf("sentinel changed: %q, %v", data, err)
	}
}

func TestCopyRefusesPlantedDestinationLink(t *testing.T) {
	sourceDir, targetDir, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "command"), []byte("synthetic"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(targetDir, "memories")); err != nil {
		t.Fatal(err)
	}
	source, err := OpenFiles(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	target, err := OpenFiles(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	if err := Copy(source, ".", target, "memories", 0); err == nil {
		t.Fatal("planted destination followed")
	}
	if _, err := os.Stat(filepath.Join(outside, "command")); !os.IsNotExist(err) {
		t.Fatal("outside command created")
	}
}

func TestRootedStoreNamesAreStableAcrossGoVersions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	opened, err := OpenFiles(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	if opened.Name() != home {
		t.Fatalf("OpenFiles name=%q want %q", opened.Name(), home)
	}
	key := backend.StoreKey{Owner: "codex", ProjectHash: "abc123"}
	store, err := OpenStore(home, key, backend.StoreKindConfig, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	want, err := ResolveDir(home, key, backend.StoreKindConfig)
	if err != nil {
		t.Fatal(err)
	}
	if store.Name() != want {
		t.Fatalf("OpenStore name=%q want %q", store.Name(), want)
	}
}
