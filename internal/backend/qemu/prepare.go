// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package qemu

import (
	"context"
	"fmt"
	"os"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

func (b *Backend) PrepareStores(ctx context.Context, prep backend.StorePrep) (backend.StoreState, error) {
	state := backend.StoreState{}
	if prep.Config != nil {
		if err := b.prepareConfigStore(ctx, *prep.Config); err != nil {
			return state, err
		}
	}
	if prep.Auth != nil {
		if err := b.storage.Ensure(ctx, prep.Auth.Key, backend.StoreKindAuth, ""); err != nil {
			logx.Warnf("Failed to prepare %s shared auth store: %v", util.TitleCase(prep.Auth.Key.Owner), err)
		}
	}
	if prep.Env != nil {
		if err := b.storage.Ensure(ctx, prep.Env.Key, backend.StoreKindEnv, ""); err != nil {
			logx.Warnf("Failed to prepare persistent %s env store: %v", model.AppName, err)
		} else if _, err := b.storage.ReadFile(ctx, prep.Env.Key, backend.StoreKindEnv, "env"); err == nil {
			state.PersistedEnvAvailable = true
		}
		if prep.Env.Reset {
			if err := b.storage.RemovePath(ctx, prep.Env.Key, backend.StoreKindEnv, "env"); err != nil && !os.IsNotExist(err) {
				logx.Warnf("Failed to reset persisted env store: %v", err)
			}
			state.PersistedEnvAvailable = false
		}
	}
	for _, feature := range prep.Features {
		if err := b.storage.Ensure(ctx, feature.Key, backend.StoreKindFeatureAuth, ""); err != nil {
			logx.Warnf("Failed to prepare %s feature auth store: %v", util.TitleCase(feature.Key.Owner), err)
		}
	}
	if prep.Config != nil && len(prep.ResetAuthFiles) > 0 {
		b.resetAuthFiles(ctx, prep)
	}
	if prep.Config != nil {
		if prep.Config.Overlay != nil && strings.TrimSpace(prep.Config.Overlay.SourceDir) != "" {
			if err := b.overlayConfigStore(*prep.Config); err != nil {
				return state, err
			}
		} else if err := hoststore.InvalidateConfigSettingsBase(b.opts.Host.Home, prep.Config.Key); err != nil {
			return state, err
		}
	}
	return state, nil
}

func (b *Backend) prepareConfigStore(ctx context.Context, prep backend.ConfigStorePrep) error {
	if prep.Recreate {
		if err := b.storage.Remove(ctx, prep.Key, backend.StoreKindConfig); err != nil && !os.IsNotExist(err) {
			logx.Debugf("Failed to remove qemu config store: %v", err)
		}
	}
	if err := b.storage.Ensure(ctx, prep.Key, backend.StoreKindConfig, ""); err != nil {
		return fmt.Errorf("prepare qemu config store: %w", err)
	}
	for _, dir := range prep.LayoutDirs {
		if err := b.createStoreDir(prep.Key, backend.StoreKindConfig, dir); err != nil {
			logx.Warnf("Failed to prepare %s config store layout: %v", util.TitleCase(prep.Key.Owner), err)
		}
	}
	return nil
}

func (b *Backend) createStoreDir(key backend.StoreKey, kind backend.StoreKind, rel string) error {
	if _, err := b.storage.MountSource(key, kind); err != nil {
		return err
	}
	name, err := b.storage.storeName(key, kind, rel)
	if err != nil {
		return err
	}
	root, err := hoststore.OpenStore(b.opts.Host.Home, key, kind, false)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	// A link or file in place of the directory is replaced, not followed.
	info, err := root.Lstat(name)
	switch {
	case err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()):
		if err := root.RemoveAll(name); err != nil {
			return err
		}
	case err != nil && !os.IsNotExist(err):
		return err
	}
	return root.MkdirAll(name, 0o700)
}

func (b *Backend) resetAuthFiles(ctx context.Context, prep backend.StorePrep) {
	authFiles, err := backend.ValidateAuthFilePaths(prep.ResetAuthFiles)
	if err != nil || len(authFiles) == 0 {
		if err != nil {
			logx.Warnf("Failed to reset %s auth files in qemu store: %v", util.TitleCase(prep.Config.Key.Owner), err)
		}
		return
	}
	for _, authFile := range authFiles {
		if err := b.storage.RemovePath(ctx, prep.Config.Key, backend.StoreKindConfig, authFile); err != nil && !os.IsNotExist(err) {
			logx.Warnf("Failed to reset config auth file %s: %v", authFile, err)
		}
		if prep.Auth != nil {
			if err := b.storage.RemovePath(ctx, prep.Auth.Key, backend.StoreKindAuth, authFile); err != nil && !os.IsNotExist(err) {
				logx.Warnf("Failed to reset shared auth file %s: %v", authFile, err)
			}
		}
	}
}

func (b *Backend) overlayConfigStore(prep backend.ConfigStorePrep) error {
	overlay := prep.Overlay
	if overlay == nil || strings.TrimSpace(overlay.SourceDir) == "" {
		return nil
	}
	root, err := b.storage.MountSource(prep.Key, backend.StoreKindConfig)
	if err != nil {
		return err
	}
	return hoststore.OverlayConfigSettings(b.opts.Host.Home, prep.Key, root, *overlay, func() error {
		return b.overlayConfigStoreContents(root, *overlay, prep.Key.Owner)
	})
}

func (b *Backend) overlayConfigStoreContents(root string, overlay backend.ConfigOverlaySpec, owner string) error {
	if err := hoststore.Overlay(root, overlay.SourceDir, overlay.PreservePaths); err != nil {
		return err
	}
	logx.Infof("%s config store populated from generated source", util.TitleCase(owner))
	return nil
}
