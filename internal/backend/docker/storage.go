// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/model"
)

type StoreManager struct {
	host model.Host
}

// WithStoreLock serializes cross-process access to one store. The lock file is
// derived from the store directory, so it stays in sync with the
// Docker-private mechanics (reconcile, reset, finalize) that lock by the same
// directory.
func (s *StoreManager) WithStoreLock(_ context.Context, key backend.StoreKey, kind backend.StoreKind, fn func() error) error {
	dir, err := s.storeDir(key, kind)
	if err != nil {
		return err
	}
	return hoststore.WithLock(s.host.Home, dir, fn)
}

// StoreExists reports whether the backing directory already exists. A genuine
// not-found is reported as absent; any other stat failure is surfaced.
func (s *StoreManager) StoreExists(_ context.Context, key backend.StoreKey, kind backend.StoreKind) (bool, error) {
	dir, err := s.storeDir(key, kind)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Ensure creates the backing directory. The directory is created as the
// invoking user, so owner is a no-op: containers already run with
// Host.UID/GID.
func (s *StoreManager) Ensure(_ context.Context, key backend.StoreKey, kind backend.StoreKind, _ string) error {
	dir, err := s.storeDir(key, kind)
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o700)
}

func (s *StoreManager) ReadFile(_ context.Context, key backend.StoreKey, kind backend.StoreKind, rel string) ([]byte, error) {
	dir, err := s.storeDir(key, kind)
	if err != nil {
		return nil, err
	}
	name, err := storeName(dir, rel, true)
	if err != nil {
		return nil, err
	}
	root, err := hoststore.OpenStore(s.host.Home, key, kind, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(name)
}

func (s *StoreManager) WriteFile(ctx context.Context, key backend.StoreKey, kind backend.StoreKind, rel string, data []byte, mode fs.FileMode) error {
	return s.writeFile(ctx, key, kind, rel, data, mode)
}

// WriteFileOwned writes a store file. Ownership is inherited from the invoking
// user, so owner is a no-op and this behaves exactly like WriteFile.
func (s *StoreManager) WriteFileOwned(ctx context.Context, key backend.StoreKey, kind backend.StoreKind, rel string, data []byte, mode fs.FileMode, _ string) error {
	return s.writeFile(ctx, key, kind, rel, data, mode)
}

func (s *StoreManager) writeFile(_ context.Context, key backend.StoreKey, kind backend.StoreKind, rel string, data []byte, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	dir, err := s.storeDir(key, kind)
	if err != nil {
		return err
	}
	name, err := storeName(dir, rel, true)
	if err != nil {
		return err
	}
	root, err := hoststore.OpenStore(s.host.Home, key, kind, true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.WriteFile(name, data, mode)
}

func (s *StoreManager) Seed(_ context.Context, key backend.StoreKey, kind backend.StoreKind, items []backend.SeedItem) error {
	if len(items) == 0 {
		return nil
	}
	dir, err := s.storeDir(key, kind)
	if err != nil {
		return err
	}
	root, err := hoststore.OpenStore(s.host.Home, key, kind, true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, item := range items {
		if strings.TrimSpace(item.HostPath) == "" || strings.TrimSpace(item.StoreRel) == "" {
			continue
		}
		name, err := storeName(dir, item.StoreRel, true)
		if err != nil {
			return err
		}
		if err := seedNode(item.HostPath, root, name, item.Mode, nil); err != nil {
			return fmt.Errorf("seed store path %q: %w", item.StoreRel, err)
		}
	}
	return nil
}

// seedNode copies the host path src to name in the store. The source is the
// user's own host tree, not the store, so links in it are followed, as before;
// on the store side no existing link is followed. ancestors holds the source
// directories being copied, to stop on a link cycle.
func seedNode(src string, dst *hoststore.Files, name string, mode fs.FileMode, ancestors []os.FileInfo) error {
	if existing, err := dst.Lstat(name); err == nil && existing.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to seed through symlinked store path %q", name)
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		for _, ancestor := range ancestors {
			if os.SameFile(ancestor, info) {
				return fmt.Errorf("symlink cycle at %s", src)
			}
		}
		if err := dst.MkdirAll(name, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := seedNode(filepath.Join(src, entry.Name()), dst, filepath.Join(name, entry.Name()), mode, append(ancestors, info)); err != nil {
				return err
			}
		}
		if err := dst.Chmod(name, info.Mode().Perm()); err != nil {
			return err
		}
	case info.Mode().IsRegular():
		data, err := os.ReadFile(src) // #nosec G304 -- src is the caller's host seed source.
		if err != nil {
			return err
		}
		fileMode := mode
		if fileMode == 0 {
			fileMode = info.Mode().Perm()
		}
		if err := dst.WriteFile(name, data, fileMode); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported seed source %s", src)
	}
	return dst.Chtimes(name, info.ModTime())
}

func (s *StoreManager) RemovePath(_ context.Context, key backend.StoreKey, kind backend.StoreKind, rel string) error {
	dir, err := s.storeDir(key, kind)
	if err != nil {
		return err
	}
	// RemovePath must be able to delete a planted symlink itself (the rooted
	// RemoveAll unlinks the symlink rather than following it), so leaf symlinks
	// are allowed here while parent-directory symlinks remain rejected.
	name, err := storeName(dir, rel, false)
	if err != nil {
		return err
	}
	root, err := hoststore.OpenStore(s.host.Home, key, kind, false)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.RemoveAll(name)
}

func (s *StoreManager) Remove(_ context.Context, key backend.StoreKey, kind backend.StoreKind) error {
	dir, err := s.storeDir(key, kind)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (s *StoreManager) storeDir(key backend.StoreKey, kind backend.StoreKind) (string, error) {
	return hoststore.ResolveDir(s.host.Home, key, kind)
}

// storeName validates a store-relative path against traversal and returns it
// cleaned, relative to the store directory dir. It rejects any parent component
// that is a symlink;
// when rejectLeafSymlink is set it also rejects a symlinked leaf, so operations
// that follow the final component (read, write, seed) cannot escape the store.
func storeName(dir string, rel string, rejectLeafSymlink bool) (string, error) {
	value := strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	if value == "" {
		return "", fmt.Errorf("store path is empty")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || path.IsAbs(cleaned) {
		return "", fmt.Errorf("invalid store path %q", rel)
	}
	target := filepath.Join(dir, filepath.FromSlash(cleaned))
	if err := hoststore.EnsureNoSymlinkChain(dir, target, false); err != nil {
		return "", err
	}
	if rejectLeafSymlink {
		if err := ensureNotSymlink(target); err != nil {
			return "", err
		}
	}
	return filepath.Rel(dir, target)
}

// ensureNotSymlink rejects target when it already exists as a symlink, so
// host-side reads/writes/seeds cannot be redirected outside the store root
// through a planted leaf symlink. A nonexistent target is fine.
func ensureNotSymlink(target string) error {
	info, err := os.Lstat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to operate through symlinked store path %q", target)
	}
	return nil
}
