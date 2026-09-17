// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package docker

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestForeignOwnedStorePathReportsOutermostMismatch(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "shared", "skills")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	me := strconv.Itoa(os.Getuid())
	other := strconv.Itoa(os.Getuid() + 1)

	if got := foreignOwnedStorePath(root, target, me); got != "" {
		t.Fatalf("foreignOwnedStorePath() = %q, want no mismatch for the invoking user", got)
	}
	// Every component is owned by the current user, so a foreign uid must flag
	// the outermost one below the store root rather than the leaf.
	if got, want := foreignOwnedStorePath(root, target, other), filepath.Join(root, "shared"); got != want {
		t.Fatalf("foreignOwnedStorePath() = %q, want %q", got, want)
	}
	if got := foreignOwnedStorePath(root, root, other); got != "" {
		t.Fatalf("foreignOwnedStorePath(root, root) = %q, want empty", got)
	}
}

func TestPrepareStoresRecreatesLayoutAfterConfigOverlay(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(Options{Host: model.Host{
		Home: t.TempDir(),
		UID:  strconv.Itoa(os.Getuid()),
		GID:  strconv.Itoa(os.Getgid()),
	}})
	key := backend.StoreKey{Owner: "claude", ProjectHash: "abc123abc123"}
	prep := backend.StorePrep{Config: &backend.ConfigStorePrep{
		Key:        key,
		LayoutDirs: []string{filepath.Join("state", "memory")},
		Overlay:    &backend.ConfigOverlaySpec{SourceDir: sourceDir},
	}}

	if _, err := b.PrepareStores(context.Background(), prep); err != nil {
		t.Fatalf("PrepareStores() error = %v", err)
	}
	storeDir, err := b.storage.storeDir(key, backend.StoreKindConfig)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(storeDir, "state", "memory")); err != nil || !info.IsDir() {
		t.Fatalf("nested layout directory was not recreated after overlay: %v", err)
	}
}
