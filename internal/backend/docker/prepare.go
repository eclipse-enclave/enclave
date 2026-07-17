// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

// PrepareStores realizes the declared stores as host directories: create or
// recreate the config store, lay out expected directories, apply reset intent,
// and overlay the generated config source. The directories are created as the
// invoking host user, so no ownership fix-up is needed (containers already run
// with Host.UID/GID). Individual preparation failures are logged and tolerated
// (a session can usually still run); only a failed config overlay aborts,
// because running against a half-populated config store would misconfigure the
// tool.
func (b *Backend) PrepareStores(ctx context.Context, prep backend.StorePrep) (backend.StoreState, error) {
	state := backend.StoreState{}
	if prep.Config != nil {
		b.prepareConfigStore(*prep.Config)
	}
	if prep.Auth != nil {
		b.ensureStoreDir(backend.StoreKindAuth, prep.Auth.Key, util.TitleCase(prep.Auth.Key.Owner)+" shared auth store")
	}
	if prep.Env != nil {
		b.ensureStoreDir(backend.StoreKindEnv, prep.Env.Key, "persistent "+model.AppName+" env store")
		state.PersistedEnvAvailable = b.envFileExists(prep.Env.Key)
	}
	for _, feature := range prep.Features {
		b.ensureStoreDir(backend.StoreKindFeatureAuth, feature.Key, util.TitleCase(feature.Key.Owner)+" feature auth store")
	}
	if prep.Env != nil && prep.Env.Reset {
		b.resetPersistedEnv(prep.Env.Key)
	}
	if prep.Config != nil && len(prep.ResetAuthFiles) > 0 {
		b.resetAuthFiles(ctx, prep)
	}
	if prep.Config != nil && prep.Config.Overlay != nil {
		if err := b.overlayConfigStore(*prep.Config); err != nil {
			return state, err
		}
	}
	return state, nil
}

func (b *Backend) prepareConfigStore(prep backend.ConfigStorePrep) {
	dir, err := b.storage.storeDir(prep.Key, backend.StoreKindConfig)
	if err != nil {
		logx.Warnf("Skipping config store preparation: %v", err)
		return
	}
	if strings.TrimSpace(prep.Key.Suffix) != "" {
		logx.Infof("Ephemeral session: Using unique store %s", dir)
	}
	if prep.Recreate {
		logx.Infof("Ephemeral session: Creating fresh store")
		if err := os.RemoveAll(dir); err != nil {
			logx.Debugf("Failed to remove config store %s: %v", dir, err)
		}
	} else if _, err := os.Stat(dir); err != nil {
		logx.Infof("Creating persistent %s config store", util.TitleCase(prep.Key.Owner))
	} else {
		logx.Infof("Reusing persistent %s config store", util.TitleCase(prep.Key.Owner))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logx.Debugf("Failed to create config store %s: %v", dir, err)
	}
	b.prepareConfigStoreLayout(dir, prep)
}

// ensureStoreDir creates the backing directory for a store if it does not
// already exist (logging "Creating <label>"), reusing it otherwise ("Reusing
// <label>").
func (b *Backend) ensureStoreDir(kind backend.StoreKind, key backend.StoreKey, label string) {
	dir, err := b.storage.storeDir(key, kind)
	if err != nil {
		logx.Debugf("Failed to resolve %s: %v", label, err)
		return
	}
	if _, err := os.Stat(dir); err != nil {
		logx.Infof("Creating %s", label)
	} else {
		logx.Infof("Reusing %s", label)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logx.Debugf("Failed to create %s: %v", label, err)
	}
}

// chownSpec returns the invoking host user's "uid:gid" ownership spec, used by
// the auth-reconcile helper container to keep reconciled files owned by the
// host user.
func (b *Backend) chownSpec() string {
	return util.ChownSpec(b.opts.Host.UID, b.opts.Host.GID)
}

func (b *Backend) envFileExists(key backend.StoreKey) bool {
	dir, err := b.storage.storeDir(key, backend.StoreKindEnv)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, "env"))
	return err == nil
}

func (b *Backend) prepareConfigStoreLayout(dir string, prep backend.ConfigStorePrep) {
	if dir == "" || len(prep.LayoutDirs) == 0 {
		return
	}
	for _, layoutDir := range prep.LayoutDirs {
		target, err := storeFilePath(dir, layoutDir, true)
		if err != nil {
			logx.Debugf("Skipping invalid config layout dir %q: %v", layoutDir, err)
			continue
		}
		if err := os.MkdirAll(target, 0o700); err != nil {
			logx.Warnf("Failed to prepare %s config store layout: %v", util.TitleCase(prep.Key.Owner), err)
		}
	}
}

func (b *Backend) resetPersistedEnv(key backend.StoreKey) {
	ctx := context.Background()
	if err := b.storage.WithStoreLock(ctx, key, backend.StoreKindEnv, func() error {
		return b.storage.RemovePath(ctx, key, backend.StoreKindEnv, "env")
	}); err != nil {
		logx.Warnf("Failed to reset persisted env store: %v", err)
	}
}

// resetAuthFiles removes the declared auth files from the config store and,
// when present, the shared auth store under a single store lock.
func (b *Backend) resetAuthFiles(ctx context.Context, prep backend.StorePrep) {
	title := util.TitleCase(prep.Config.Key.Owner)
	authFiles, err := backend.ValidateAuthFilePaths(prep.ResetAuthFiles)
	if err != nil || len(authFiles) == 0 {
		if err != nil {
			logx.Warnf("Failed to reset %s auth files in store: %v", title, err)
		}
		return
	}

	lockKey := prep.Config.Key
	lockKind := backend.StoreKindConfig
	if prep.Auth != nil {
		lockKey = prep.Auth.Key
		lockKind = backend.StoreKindAuth
	}

	if err := b.storage.WithStoreLock(ctx, lockKey, lockKind, func() error {
		logx.Infof("Resetting %s auth files in store", title)
		for _, authFile := range authFiles {
			if err := b.storage.RemovePath(ctx, prep.Config.Key, backend.StoreKindConfig, authFile); err != nil {
				return err
			}
			if prep.Auth != nil {
				if err := b.storage.RemovePath(ctx, prep.Auth.Key, backend.StoreKindAuth, authFile); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		logx.Warnf("Failed to reset %s auth files in store: %v", title, err)
	}
}

// overlayConfigStore wipes the config store and copies the generated host-side
// config source into it, preserving the declared session-state paths. This
// runs before auth hooks so hook-written state lands on top of the generated
// config and survives into the session.
func (b *Backend) overlayConfigStore(prep backend.ConfigStorePrep) error {
	overlay := prep.Overlay
	if overlay == nil || strings.TrimSpace(overlay.SourceDir) == "" {
		return nil
	}
	dir, err := b.storage.storeDir(prep.Key, backend.StoreKindConfig)
	if err != nil {
		return err
	}
	if err := overlayConfigDir(dir, overlay.SourceDir, overlay.PreservePaths); err != nil {
		return fmt.Errorf("populate config store from source: %w", err)
	}
	logx.Infof("%s config store populated from generated source", util.TitleCase(prep.Key.Owner))
	return nil
}
