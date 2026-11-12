// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package testutil

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
)

const storeSwapRounds = 1000

// ExerciseStoreDuringSwaps runs reads, writes, seeds, and removals through
// session while a concurrent writer keeps replacing the store directory
// "agent" with a link to an outside directory and other writes the same store.
// It fails the test if session reads outside content or anything outside the
// store changes. session and other must share the host home.
func ExerciseStoreDuringSwaps(t *testing.T, home string, session, other backend.StoreManager) {
	t.Helper()
	ctx := context.Background()
	key := backend.StoreKey{Owner: "tool", ProjectHash: "abc123def456"}
	kind := backend.StoreKindConfig
	if err := session.Ensure(ctx, key, kind, ""); err != nil {
		t.Fatal(err)
	}
	storeDir := hoststore.Dir(home, key, kind)
	agent, saved := filepath.Join(storeDir, "agent"), filepath.Join(storeDir, "saved")
	if err := os.Mkdir(agent, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	for name, content := range map[string]string{"auth": "outside", "stale": "outside"} {
		if err := os.WriteFile(filepath.Join(outside, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.WriteFile(seed, []byte("seeded"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := PinTree(t, outside)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range storeSwapRounds {
			_ = os.Rename(agent, saved)
			_ = os.Symlink(outside, agent)
			_ = os.Remove(agent)
			_ = os.Rename(saved, agent)
		}
	}()
	go func() {
		defer wg.Done()
		for range storeSwapRounds {
			_ = other.WriteFile(ctx, key, kind, "agent/other", []byte("other session"), 0o600)
		}
	}()
	for range storeSwapRounds {
		_ = session.WriteFile(ctx, key, kind, "agent/auth", []byte("inside"), 0o600)
		if data, err := session.ReadFile(ctx, key, kind, "agent/auth"); err == nil && string(data) != "inside" {
			t.Errorf("read outside content: %q", data)
		}
		_ = session.Seed(ctx, key, kind, []backend.SeedItem{{HostPath: seed, StoreRel: "agent/seeded"}})
		_ = session.RemovePath(ctx, key, kind, "agent/stale")
	}
	wg.Wait()
	AssertTreeUnchanged(t, outside, before)
}

// ExerciseLayoutDuringSwaps calls prepare, which creates "agent/projects" in
// storeDir, while a concurrent writer keeps replacing "agent" with a link to
// an outside directory. It fails the test if anything outside the store
// changes.
func ExerciseLayoutDuringSwaps(t *testing.T, storeDir string, prepare func()) {
	t.Helper()
	agent, saved := filepath.Join(storeDir, "agent"), filepath.Join(storeDir, "saved")
	if err := os.MkdirAll(agent, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	before := PinTree(t, outside)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Rename(agent, saved)
			_ = os.Symlink(outside, agent)
			_ = os.Remove(agent)
			_ = os.Rename(saved, agent)
		}
	}()
	for range storeSwapRounds {
		prepare()
	}
	close(stop)
	<-done
	AssertTreeUnchanged(t, outside, before)
}
