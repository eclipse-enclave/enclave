// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/auth"
	"enclave/internal/backend"
	dockerbackend "enclave/internal/backend/docker"
	"enclave/internal/backend/hoststore"
	qemubackend "enclave/internal/backend/qemu"
	"enclave/internal/model"
)

type hookStore struct {
	backend.StoreManager
	files    map[backend.StoreRef]map[string][]byte
	writeErr error
}

func (s *hookStore) ReadFile(_ context.Context, key backend.StoreKey, kind backend.StoreKind, rel string) ([]byte, error) {
	data, ok := s.files[backend.StoreRef{Key: key, Kind: kind}][rel]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func (s *hookStore) WriteFile(_ context.Context, key backend.StoreKey, kind backend.StoreKind, rel string, data []byte, _ fs.FileMode) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	ref := backend.StoreRef{Key: key, Kind: kind}
	if s.files[ref] == nil {
		s.files[ref] = map[string][]byte{}
	}
	s.files[ref][rel] = append([]byte(nil), data...)
	return nil
}

func (*hookStore) RemovePath(context.Context, backend.StoreKey, backend.StoreKind, string) error {
	return nil
}

func TestAfterEnvInjectedDoesNotPersistAPIKey(t *testing.T) {
	store := &backend.StoreRef{Kind: backend.StoreKindAuth, Key: backend.StoreKey{Owner: "claude"}}
	storage := &hookStore{files: map[backend.StoreRef]map[string][]byte{}}
	ctx := auth.Context{Storage: storage, AuthStorage: store}
	if err := (claudeHooks{}).AfterEnvInjected(ctx, map[string]string{"ANTHROPIC_API_KEY": "synthetic-secret"}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range claudeConfigFiles {
		var got map[string]any
		if err := json.Unmarshal(storage.files[*store][rel], &got); err != nil {
			t.Fatal(err)
		}
		if got[onboardingMarker] != true {
			t.Fatalf("%s: onboarding missing", rel)
		}
		for _, key := range []string{"anthropicApiKey", "primaryApiKey"} {
			if _, ok := got[key]; ok {
				t.Fatalf("%s: persisted %s", rel, key)
			}
		}
	}
}

func TestOnAuthReadyScrubsExistingStoresWithoutInjectedKey(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "project", true: "shared"}[shared], func(t *testing.T) {
			config := backend.StoreRef{Kind: backend.StoreKindConfig, Key: backend.StoreKey{Owner: "claude", ProjectHash: "project"}}
			authStore := config
			if shared {
				authStore = backend.StoreRef{Kind: backend.StoreKindAuth, Key: backend.StoreKey{Owner: "claude", Suffix: "named"}}
			}
			storage := &hookStore{files: map[backend.StoreRef]map[string][]byte{}}
			original := []byte(`{"hasCompletedOnboarding":true,"anthropicApiKey":"old-key","primaryApiKey":"old-key","oauthAccount":{"id":"keep"},"large":9007199254740993}`)
			for _, store := range []backend.StoreRef{config, authStore} {
				storage.files[store] = map[string][]byte{}
				for _, rel := range claudeConfigFiles {
					storage.files[store][rel] = original
				}
			}
			ctx := auth.Context{Storage: storage, ConfigStore: &config, AuthStorage: &authStore}
			// No provider session or injected key: cleanup must precede either shortcut.
			if _, err := (claudeHooks{}).OnAuthReady(ctx); err != nil {
				t.Fatal(err)
			}
			for _, store := range []backend.StoreRef{config, authStore} {
				for _, rel := range claudeConfigFiles {
					var got map[string]json.RawMessage
					if err := json.Unmarshal(storage.files[store][rel], &got); err != nil {
						t.Fatal(err)
					}
					if _, ok := got["anthropicApiKey"]; ok {
						t.Fatal("legacy anthropicApiKey survived cleanup")
					}
					if _, ok := got["primaryApiKey"]; ok {
						t.Fatal("legacy primaryApiKey survived cleanup")
					}
					if string(got["oauthAccount"]) != `{"id":"keep"}` || string(got["large"]) != "9007199254740993" || string(got[onboardingMarker]) != "true" {
						t.Fatalf("unrelated state changed: %s", storage.files[store][rel])
					}
				}
			}
		})
	}
}

func TestOnAuthReadyReportsCleanupFailure(t *testing.T) {
	store := backend.StoreRef{Kind: backend.StoreKindAuth, Key: backend.StoreKey{Owner: "claude"}}
	want := errors.New("write refused")
	storage := &hookStore{
		files:    map[backend.StoreRef]map[string][]byte{store: {"config.json": []byte(`{"hasCompletedOnboarding":true,"primaryApiKey":"old-key"}`)}},
		writeErr: want,
	}
	if _, err := (claudeHooks{}).OnAuthReady(auth.Context{Storage: storage, AuthStorage: &store}); !errors.Is(err, want) {
		t.Fatalf("cleanup error = %v, want %v", err, want)
	}
}

func TestOnAuthReadyHandlesOnlyManagedSharedAuthLinks(t *testing.T) {
	for _, engine := range []string{"docker", "qemu"} {
		for _, managed := range []bool{true, false} {
			name := "managed"
			if !managed {
				name = "untrusted"
			}
			t.Run(engine+"/"+name, func(t *testing.T) {
				home := t.TempDir()
				for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
					t.Setenv(key, filepath.Join(home, key))
				}
				host := model.Host{Home: home}
				storage := dockerbackend.New(dockerbackend.Options{Host: host}).Storage()
				if engine == "qemu" {
					storage = qemubackend.New(qemubackend.Options{Host: host}).Storage()
				}
				config := backend.StoreRef{Kind: backend.StoreKindConfig, Key: backend.StoreKey{Owner: "claude", ProjectHash: "project"}}
				authStore := backend.StoreRef{Kind: backend.StoreKindAuth, Key: backend.StoreKey{Owner: "claude"}}
				for _, store := range []backend.StoreRef{config, authStore} {
					if err := storage.Ensure(context.Background(), store.Key, store.Kind, ""); err != nil {
						t.Fatal(err)
					}
				}
				old := []byte(`{"hasCompletedOnboarding":true,"primaryApiKey":"synthetic-old-key","keep":true}`)
				if err := storage.WriteFile(context.Background(), authStore.Key, authStore.Kind, "config.json", old, 0o600); err != nil {
					t.Fatal(err)
				}
				configDir, err := hoststore.DirFor(home, config.Key, config.Kind)
				if err != nil {
					t.Fatal(err)
				}
				target := "/home/agent/" + model.ContainerAuthDir + "/config.json"
				if !managed {
					target = filepath.Join(t.TempDir(), "outside-config.json")
					if err := os.WriteFile(target, old, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, filepath.Join(configDir, "config.json")); err != nil {
					t.Fatal(err)
				}
				ctx := auth.Context{
					Storage: storage, ConfigStore: &config, AuthStorage: &authStore,
					ContainerHome: "/home/agent",
					Profile:       model.Profile{Providers: []model.ProviderConfig{{AuthFiles: []string{"config.json"}}}},
				}
				_, err = (claudeHooks{}).OnAuthReady(ctx)
				if !managed {
					if err == nil {
						t.Fatal("accepted an arbitrary config-store symlink")
					}
					if !strings.Contains(err.Error(), `refusing symlinked store file "config.json"`) {
						t.Fatalf("refusal does not name the store file: %v", err)
					}
					data, readErr := os.ReadFile(target)
					if readErr != nil || string(data) != string(old) {
						t.Fatalf("changed arbitrary symlink target: %v", readErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("returning shared-auth session failed: %v", err)
				}
				data, err := storage.ReadFile(context.Background(), authStore.Key, authStore.Kind, "config.json")
				if err != nil || strings.Contains(string(data), "synthetic-old-key") {
					t.Fatalf("shared auth was not scrubbed: %v", err)
				}
				// The same project then uses project auth: the config store is
				// the auth store and still holds the link.
				ctx.AuthStorage = &config
				if _, err := (claudeHooks{}).OnAuthReady(ctx); err != nil {
					t.Fatalf("session with project auth after shared auth failed: %v", err)
				}
				if link, err := os.Readlink(filepath.Join(configDir, "config.json")); err != nil || link != target {
					t.Fatalf("managed alias changed: %v", err)
				}
			})
		}
	}
}
