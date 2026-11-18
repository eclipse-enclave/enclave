// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vmhost

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/logx"
)

func (s *StoreManager) SyncRequestAuth(req backend.Request) {
	if req.AuthSync == nil {
		return
	}
	ctx := context.Background()
	var config *backend.PersistentStore
	var auth *backend.PersistentStore
	for i := range req.Stores {
		switch req.Stores[i].Kind {
		case backend.StoreKindConfig:
			store := req.Stores[i]
			config = &store
		case backend.StoreKindAuth:
			store := req.Stores[i]
			auth = &store
		}
	}
	if config == nil {
		return
	}
	placeholders := backend.SecretPlaceholders(req.Secrets)
	if auth != nil && len(req.AuthSync.AuthFiles) > 0 {
		if err := s.SyncSharedAuth(ctx, config.Key, auth.Key, req.AuthSync.AuthFiles, placeholders...); err != nil {
			logx.Warnf("Failed to sync %s shared auth files: %v", s.label, err)
		}
	}
	for _, sync := range req.AuthSync.Features {
		if err := s.SyncFeatureAuth(ctx, config.Key, sync, placeholders...); err != nil {
			logx.Warnf("Failed to sync %s %s feature auth files: %v", sync.Feature, s.label, err)
		}
	}
}

func (s *StoreManager) SyncSharedAuth(ctx context.Context, configKey backend.StoreKey, authKey backend.StoreKey, files []string, placeholders ...string) error {
	validated, err := backend.ValidateAuthFilePaths(files)
	if err != nil {
		return err
	}
	for _, file := range validated {
		data, err := s.ReadFile(ctx, configKey, backend.StoreKindConfig, file)
		if err != nil || len(bytes.TrimSpace(data)) == 0 {
			continue
		}
		if backend.ContainsSecretPlaceholder(data, placeholders) {
			logx.Warnf("Not syncing auth file %s: contains a gateway placeholder.", file)
			continue
		}
		current, err := s.ReadFile(ctx, authKey, backend.StoreKindAuth, file)
		if err == nil && len(bytes.TrimSpace(current)) > 0 {
			continue
		}
		if err := s.WriteFile(ctx, authKey, backend.StoreKindAuth, file, data, fs.FileMode(0o600)); err != nil {
			return fmt.Errorf("copy auth file %s: %w", file, err)
		}
	}
	return nil
}

func (s *StoreManager) SyncFeatureAuth(ctx context.Context, configKey backend.StoreKey, sync backend.FeatureAuthSync, placeholders ...string) error {
	feature := strings.TrimSpace(sync.Feature)
	if feature == "" {
		return nil
	}
	validated, err := backend.ValidateAuthFilePaths(sync.AuthFiles)
	if err != nil {
		return err
	}
	featureKey := backend.StoreKey{Owner: feature}
	for _, file := range validated {
		sourceRel := filepath.ToSlash(filepath.Join(sync.ConfigDir, file))
		data, err := s.ReadFile(ctx, configKey, backend.StoreKindConfig, sourceRel)
		if err != nil || len(bytes.TrimSpace(data)) == 0 {
			continue
		}
		if backend.ContainsSecretPlaceholder(data, placeholders) {
			logx.Warnf("Not syncing %s auth file %s: contains a gateway placeholder.", feature, file)
			continue
		}
		current, err := s.ReadFile(ctx, featureKey, backend.StoreKindFeatureAuth, file)
		if err == nil && len(bytes.TrimSpace(current)) > 0 {
			continue
		}
		if err := s.WriteFile(ctx, featureKey, backend.StoreKindFeatureAuth, file, data, fs.FileMode(0o600)); err != nil {
			return fmt.Errorf("copy feature auth file %s: %w", file, err)
		}
	}
	return nil
}
