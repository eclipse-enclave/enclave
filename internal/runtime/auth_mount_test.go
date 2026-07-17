// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

// TestAddAuthMountSetsSecurestorageDirEnv verifies that a profile declaring a
// securestorage_dir_env gets that variable pointed at the shared auth store
// mount, so the tool (Claude) stores credentials there natively.
func TestAddAuthMountSetsSecurestorageDirEnv(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		profile: model.Profile{
			Name:      "claude",
			ConfigDir: ".claude",
			Providers: []model.ProviderConfig{{
				Name:                "anthropic",
				AuthFiles:           []string{"config.json", ".credentials.json"},
				SecurestorageDirEnv: "CLAUDE_SECURESTORAGE_CONFIG_DIR",
			}},
		},
		containerHome: "/home/agent",
	}

	acc := newMountAccumulator(nil, nil)
	r.addAuthMount(acc, &backend.StoreRef{Kind: backend.StoreKindAuth, Key: backend.StoreKey{Owner: "claude"}})

	wantPath := "/home/agent/" + model.ContainerAuthDir
	got, ok := lookupEnv(acc.Env(), "CLAUDE_SECURESTORAGE_CONFIG_DIR")
	if !ok || got != wantPath {
		t.Fatalf("expected CLAUDE_SECURESTORAGE_CONFIG_DIR=%s, got %q (present=%v)", wantPath, got, ok)
	}
	// The securestorage dir must be the mounted auth store target so the tool
	// reads/writes its credential file in the shared location.
	if !hasStore(acc.Stores(), backend.StoreKindAuth, "claude", wantPath) {
		t.Fatalf("expected auth store mounted at %s", wantPath)
	}
}

// TestAddAuthMountOmitsSecurestorageDirEnvWhenUnset verifies tools without a
// securestorage_dir_env (the common case) get no such variable.
func hasStore(stores []backend.PersistentStore, kind backend.StoreKind, owner string, containerPath string) bool {
	for _, store := range stores {
		if store.Kind == kind && store.Key.Owner == owner && store.ContainerPath == containerPath {
			return true
		}
	}
	return false
}

func TestAddAuthMountOmitsSecurestorageDirEnvWhenUnset(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		profile: model.Profile{
			Name:      "codex",
			ConfigDir: ".codex",
			Providers: []model.ProviderConfig{{
				Name:      "openai",
				AuthFiles: []string{"auth.json"},
			}},
		},
		containerHome: "/home/agent",
	}

	acc := newMountAccumulator(nil, nil)
	r.addAuthMount(acc, &backend.StoreRef{Kind: backend.StoreKindAuth, Key: backend.StoreKey{Owner: "codex"}})

	if _, ok := lookupEnv(acc.Env(), "CLAUDE_SECURESTORAGE_CONFIG_DIR"); ok {
		t.Fatalf("did not expect CLAUDE_SECURESTORAGE_CONFIG_DIR for codex")
	}
}
