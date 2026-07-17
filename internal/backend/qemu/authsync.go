// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package qemu

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

func (b *Backend) runRequestAuthSync(req backend.Request) {
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
	if auth != nil && len(req.AuthSync.AuthFiles) > 0 {
		if err := b.syncSharedAuth(ctx, config.Key, auth.Key, req.AuthSync.AuthFiles); err != nil {
			logx.Warnf("Failed to sync qemu shared auth files: %v", err)
		}
	}
	for _, sync := range req.AuthSync.Features {
		if err := b.syncFeatureAuth(ctx, config.Key, sync); err != nil {
			logx.Warnf("Failed to sync %s qemu feature auth files: %v", sync.Feature, err)
		}
	}
}

func (b *Backend) syncSharedAuth(ctx context.Context, configKey backend.StoreKey, authKey backend.StoreKey, files []string) error {
	validated, err := backend.ValidateAuthFilePaths(files)
	if err != nil {
		return err
	}
	for _, file := range validated {
		data, err := b.storage.ReadFile(ctx, configKey, backend.StoreKindConfig, file)
		if err != nil || len(bytes.TrimSpace(data)) == 0 {
			continue
		}
		current, err := b.storage.ReadFile(ctx, authKey, backend.StoreKindAuth, file)
		if err == nil && len(bytes.TrimSpace(current)) > 0 {
			continue
		}
		if err := b.storage.WriteFile(ctx, authKey, backend.StoreKindAuth, file, data, fs.FileMode(0o600)); err != nil {
			return fmt.Errorf("copy auth file %s: %w", file, err)
		}
	}
	return nil
}

func (b *Backend) syncFeatureAuth(ctx context.Context, configKey backend.StoreKey, sync backend.FeatureAuthSync) error {
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
		data, err := b.storage.ReadFile(ctx, configKey, backend.StoreKindConfig, sourceRel)
		if err != nil || len(bytes.TrimSpace(data)) == 0 {
			continue
		}
		current, err := b.storage.ReadFile(ctx, featureKey, backend.StoreKindFeatureAuth, file)
		if err == nil && len(bytes.TrimSpace(current)) > 0 {
			continue
		}
		if err := b.storage.WriteFile(ctx, featureKey, backend.StoreKindFeatureAuth, file, data, fs.FileMode(0o600)); err != nil {
			return fmt.Errorf("copy feature auth file %s: %w", file, err)
		}
	}
	return nil
}
