// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

// lockingEnvStore serves one env file and records whether it was read under
// the store lock.
type lockingEnvStore struct {
	env          string
	locked       bool
	reads        int
	unlockedRead bool
}

func (s *lockingEnvStore) WithStoreLock(_ context.Context, key backend.StoreKey, kind backend.StoreKind, fn func() error) error {
	if kind != backend.StoreKindEnv || key != EnvStoreKey("claude", "abc123abc123") {
		return errors.New("unexpected store")
	}
	s.locked = true
	defer func() { s.locked = false }()
	return fn()
}

func (s *lockingEnvStore) ReadFile(_ context.Context, _ backend.StoreKey, _ backend.StoreKind, rel string) ([]byte, error) {
	s.reads++
	if !s.locked {
		s.unlockedRead = true
	}
	if rel != "env" || s.env == "" {
		return nil, fs.ErrNotExist
	}
	return []byte(s.env), nil
}

func (s *lockingEnvStore) Ensure(context.Context, backend.StoreKey, backend.StoreKind, string) error {
	return nil
}
func (s *lockingEnvStore) WriteFile(context.Context, backend.StoreKey, backend.StoreKind, string, []byte, fs.FileMode) error {
	return nil
}
func (s *lockingEnvStore) Seed(context.Context, backend.StoreKey, backend.StoreKind, []backend.SeedItem) error {
	return nil
}
func (s *lockingEnvStore) RemovePath(context.Context, backend.StoreKey, backend.StoreKind, string) error {
	return nil
}
func (s *lockingEnvStore) Remove(context.Context, backend.StoreKey, backend.StoreKind) error {
	return nil
}

func TestReadSecretSources(t *testing.T) {
	home := t.TempDir()
	const hash = "abc123abc123"
	writeSecretsTestFile(t, config.HostSecretsProjectFile(home, hash, "claude"), "FROM_SECRETS=1\n")
	store := &lockingEnvStore{env: "FROM_STORE=2\n"}
	request := func(edit func(*SecretSourcesRequest)) SecretSourcesRequest {
		req := SecretSourcesRequest{
			Home:        home,
			ProjectHash: hash,
			Tool:        "claude",
			Run:         model.RunOptions{Persist: true},
			Auth:        model.AuthOptions{SecretsScope: model.SecretsScopeProject},
			Storage:     store,
		}
		if edit != nil {
			edit(&req)
		}
		return req
	}

	sources := ReadSecretSources(request(nil))
	if len(sources.Layers) != 1 || sources.Layers[0].Values["FROM_SECRETS"] != "1" || sources.Persisted["FROM_STORE"] != "2" {
		t.Fatalf("sources = %+v", sources)
	}
	if store.reads != 1 || store.unlockedRead {
		t.Fatalf("env store reads = %d, unlocked = %v, want one read under the lock", store.reads, store.unlockedRead)
	}

	// Neither an ephemeral run nor --reset-auth reads the env store.
	for name, edit := range map[string]func(*SecretSourcesRequest){
		"ephemeral":  func(r *SecretSourcesRequest) { r.Run.Persist = false },
		"reset auth": func(r *SecretSourcesRequest) { r.Auth.ResetAuth = true },
		"no storage": func(r *SecretSourcesRequest) { r.Storage = nil },
	} {
		store.reads = 0
		sources := ReadSecretSources(request(edit))
		if store.reads != 0 || len(sources.Persisted) != 0 || sources.Persisted == nil || len(sources.Layers) != 1 {
			t.Errorf("%s: reads = %d, sources = %+v", name, store.reads, sources)
		}
	}
}

// The startup check of filter credentials and the session read the same
// sources, so an unreadable secrets file warns once.
func TestReadSecretSourcesWarnsOnce(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(config.HostSecretsProjectFile(home, "abc123abc123", "warn-once"), 0o700); err != nil {
		t.Fatal(err)
	}
	req := SecretSourcesRequest{Home: home, ProjectHash: "abc123abc123", Tool: "warn-once", Auth: model.AuthOptions{SecretsScope: model.SecretsScopeProject}}
	stderr := captureTestStderr(t, func() {
		ReadSecretSources(req)
		ReadSecretSources(req)
	})
	if count := strings.Count(stderr, "Failed to read layered secrets"); count != 1 {
		t.Fatalf("warnings = %d, want 1\n%s", count, stderr)
	}
}

func writeSecretsTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func captureTestStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	os.Stderr = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}
