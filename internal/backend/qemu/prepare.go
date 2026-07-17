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
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/backend"
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
	if prep.Config != nil && prep.Config.Overlay != nil {
		if err := b.overlayConfigStore(*prep.Config); err != nil {
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
	target, err := b.storage.storeRelPath(key, kind, rel)
	if err != nil {
		return err
	}
	return ensureCopyDir(target, 0o700)
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
	preserveDir, err := os.MkdirTemp("", "enclave-qemu-preserve-*")
	if err != nil {
		return fmt.Errorf("create config preserve dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(preserveDir) }()

	matches, err := qemuPreserveMatches(root, overlay.PreservePaths)
	if err != nil {
		return fmt.Errorf("resolve config preserve paths: %w", err)
	}
	preserved := make([]string, 0, len(matches))
	for _, cleaned := range matches {
		src := filepath.Join(root, filepath.FromSlash(cleaned))
		dst := filepath.Join(preserveDir, filepath.FromSlash(cleaned))
		if err := copyPath(src, dst, 0); err != nil {
			return fmt.Errorf("preserve config path %s: %w", cleaned, err)
		}
		preserved = append(preserved, cleaned)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read config store: %w", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("clear config store: %w", err)
		}
	}
	if err := util.CopyTree(overlay.SourceDir, root, copyFile); err != nil {
		return fmt.Errorf("populate config store from source: %w", err)
	}
	for _, cleaned := range preserved {
		src := filepath.Join(preserveDir, filepath.FromSlash(cleaned))
		dst := filepath.Join(root, filepath.FromSlash(cleaned))
		if err := copyPath(src, dst, 0); err != nil {
			return fmt.Errorf("restore config path %s: %w", cleaned, err)
		}
	}
	logx.Infof("%s config store populated from generated source", util.TitleCase(prep.Key.Owner))
	return nil
}

// qemuPreserveMatches resolves the overlay preserve policy against the current
// store contents, mirroring the Docker overlay matcher: entries may be exact
// paths, directories (trailing "/"), or globs (containing "*?["). It returns
// the existing store-relative paths to preserve. Lookups use Lstat and WalkDir
// (both no-follow), so guest symlinks are matched but never dereferenced.
func qemuPreserveMatches(root string, preservePaths []string) ([]string, error) {
	var dirs, exacts, globs []string
	for _, raw := range preservePaths {
		value := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
		if value == "" {
			continue
		}
		isDir := strings.HasSuffix(value, "/")
		isGlob := strings.ContainsAny(value, "*?[")
		cleaned, err := cleanStoreRel(strings.TrimSuffix(value, "/"))
		if err != nil {
			continue
		}
		switch {
		case isGlob:
			globs = append(globs, cleaned)
		case isDir:
			dirs = append(dirs, cleaned)
		default:
			exacts = append(exacts, cleaned)
		}
	}

	matched := make(map[string]struct{})
	for _, rel := range append(append([]string{}, exacts...), dirs...) {
		// Reject a guest-planted symlink on any parent component before Lstat,
		// which would otherwise follow it (e.g. "agent" -> host dir in
		// "agent/auth.json"). The glob walk below is already no-follow.
		if err := verifyStoreParentsNoFollow(root, rel); err != nil {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			matched[rel] = struct{}{}
		}
	}
	if len(globs) > 0 {
		walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == root {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			for _, g := range globs {
				if matchGlob(g, rel) || matchGlob(g, d.Name()) {
					matched[rel] = struct{}{}
					break
				}
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}

	out := make([]string, 0, len(matched))
	for rel := range matched {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

func matchGlob(pattern string, name string) bool {
	ok, err := filepath.Match(pattern, name)
	return err == nil && ok
}
