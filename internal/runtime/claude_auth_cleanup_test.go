// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "enclave/extensions/tools/claude/go"
	"enclave/internal/backend"
	dockerbackend "enclave/internal/backend/docker"
	"enclave/internal/backend/hoststore"
	"enclave/internal/model"
)

func TestPrepareReturningClaudeSessionScrubsSharedStore(t *testing.T) {
	r := runtimeWithProfile(t, model.Profile{
		Name:      "claude",
		Providers: []model.ProviderConfig{{Name: "anthropic", AuthFiles: []string{"config.json"}}},
	})
	r.containerHome = "/home/agent"
	r.host.UID, r.host.GID = "1000", "1000"
	storage := dockerbackend.New(dockerbackend.Options{Host: r.host}).Storage()
	r.backend = &fakeBackend{storage: storage}
	configStore := backend.StoreRef{Kind: backend.StoreKindConfig, Key: backend.StoreKey{Owner: "claude", ProjectHash: r.project.Hash}}
	authStore := backend.StoreRef{Kind: backend.StoreKindAuth, Key: backend.StoreKey{Owner: "claude"}}
	ctx := context.Background()
	if err := storage.Ensure(ctx, configStore.Key, configStore.Kind, ""); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteFile(ctx, authStore.Key, authStore.Kind, "config.json", []byte(`{"hasCompletedOnboarding":true,"primaryApiKey":"synthetic-old-key","keep":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := hoststore.DirFor(r.host.Home, configStore.Key, configStore.Kind)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/home/agent/"+model.ContainerAuthDir+"/config.json", filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
	var env []string
	stores := storeSet{Config: &configStore, Auth: &authStore}
	var previous []byte
	for run := 0; run < 2; run++ {
		if _, _, err := newAuthManager(r).Prepare(&env, stores); err != nil {
			t.Fatalf("returning session %d: %v", run, err)
		}
		data, err := storage.ReadFile(ctx, authStore.Key, authStore.Kind, "config.json")
		if err != nil || strings.Contains(string(data), "synthetic-old-key") || !strings.Contains(string(data), `"keep":true`) {
			t.Fatalf("legacy shared credential not safely cleaned: %v", err)
		}
		if run != 0 && string(data) != string(previous) {
			t.Fatal("cleanup was not idempotent")
		}
		previous = data
	}
}
