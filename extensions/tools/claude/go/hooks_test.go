// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package claude

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"testing"

	"enclave/internal/auth"
	"enclave/internal/backend"
)

// linkedStore refuses rels it treats as links, like the host store does for
// a protected session's config.json link.
type linkedStore struct {
	files  map[string][]byte
	linked map[string]bool
}

func (s *linkedStore) Ensure(context.Context, backend.StoreKey, backend.StoreKind, string) error {
	return nil
}
func (s *linkedStore) ReadFile(_ context.Context, _ backend.StoreKey, _ backend.StoreKind, rel string) ([]byte, error) {
	if s.linked[rel] {
		return nil, errors.New("refusing to operate through symlinked store path")
	}
	if data, ok := s.files[rel]; ok {
		return data, nil
	}
	return nil, fs.ErrNotExist
}
func (s *linkedStore) WriteFile(_ context.Context, _ backend.StoreKey, _ backend.StoreKind, rel string, data []byte, _ fs.FileMode) error {
	if s.linked[rel] {
		return errors.New("refusing to operate through symlinked store path")
	}
	s.files[rel] = data
	return nil
}
func (s *linkedStore) Seed(context.Context, backend.StoreKey, backend.StoreKind, []backend.SeedItem) error {
	return nil
}
func (s *linkedStore) RemovePath(context.Context, backend.StoreKey, backend.StoreKind, string) error {
	return nil
}
func (s *linkedStore) Remove(context.Context, backend.StoreKey, backend.StoreKind) error { return nil }

func TestOnboardingMarkerToleratesLinkedConfigFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		linked  []string
		wantErr bool
	}{
		{name: "ordinary store"},
		{name: "protected config.json link", linked: []string{"config.json"}},
		{name: "every file linked", linked: []string{"config.json", ".claude.json"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &linkedStore{files: map[string][]byte{}, linked: map[string]bool{}}
			for _, rel := range tc.linked {
				store.linked[rel] = true
			}
			ref := &backend.StoreRef{Kind: backend.StoreKindConfig}
			err := ensureOnboardingComplete(auth.Context{Storage: store}, ref)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%t", err, tc.wantErr)
			}
			for _, rel := range claudeConfigFiles {
				marked := bytes.Contains(store.files[rel], []byte(onboardingMarker))
				if marked == store.linked[rel] {
					t.Fatalf("%s: marked=%t linked=%t", rel, marked, store.linked[rel])
				}
			}
		})
	}
}
