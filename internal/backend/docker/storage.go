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
	target, err := storeFilePath(dir, rel, true)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(target) // #nosec G304 -- target is validated by storeFilePath against traversal.
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
	target, err := storeFilePath(dir, rel, true)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create store dir: %w", err)
	}
	return atomicWriteFile(target, data, mode)
}

func (s *StoreManager) Seed(_ context.Context, key backend.StoreKey, kind backend.StoreKind, items []backend.SeedItem) error {
	if len(items) == 0 {
		return nil
	}
	dir, err := s.storeDir(key, kind)
	if err != nil {
		return err
	}
	for _, item := range items {
		if strings.TrimSpace(item.HostPath) == "" || strings.TrimSpace(item.StoreRel) == "" {
			continue
		}
		target, err := storeFilePath(dir, item.StoreRel, true)
		if err != nil {
			return err
		}
		if err := seedStorePath(item.HostPath, target, item.Mode); err != nil {
			return err
		}
	}
	return nil
}

func (s *StoreManager) RemovePath(_ context.Context, key backend.StoreKey, kind backend.StoreKind, rel string) error {
	dir, err := s.storeDir(key, kind)
	if err != nil {
		return err
	}
	// RemovePath must be able to delete a planted symlink itself (os.RemoveAll
	// unlinks the symlink rather than following it), so leaf symlinks are
	// allowed here while parent-directory symlinks remain rejected.
	target, err := storeFilePath(dir, rel, false)
	if err != nil {
		return err
	}
	return os.RemoveAll(target)
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

// atomicWriteFile writes data to target via a temp file in the same directory
// followed by an atomic rename.
func atomicWriteFile(target string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".enclave-store-*")
	if err != nil {
		return fmt.Errorf("create temp store file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) } // #nosec G703 -- tmpPath is created by os.CreateTemp.
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp store file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp store file: %w", err)
	}
	if err := os.Chmod(tmpPath, mode.Perm()); err != nil { // #nosec G703 -- tmpPath is created by os.CreateTemp in the validated store dir.
		cleanup()
		return fmt.Errorf("chmod temp store file: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil { // #nosec G703 -- target is validated against traversal/symlinks by storeFilePath; tmpPath is a sibling temp file.
		cleanup()
		return fmt.Errorf("move store file into place: %w", err)
	}
	return nil
}

// seedStorePath copies src (file or directory tree) into target, applying mode
// recursively when it is non-zero. The copy is symlink-safe: it refuses to
// create or write through any destination component that already exists as a
// symlink, so a planted symlink anywhere in the destination tree cannot
// redirect a seed outside the store.
func seedStorePath(src string, target string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create store dir: %w", err)
	}
	if err := copyTreeNoFollow(src, target); err != nil {
		return fmt.Errorf("seed store path %q: %w", target, err)
	}
	if mode != 0 {
		if err := chmodTree(target, mode.Perm()); err != nil {
			return fmt.Errorf("apply seed mode: %w", err)
		}
	}
	return nil
}

// copyTreeNoFollow recursively copies src into dst, verifying that each
// destination node is not a pre-existing symlink before creating a directory or
// writing a file at that path. Source symlinks are dereferenced (via os.Stat)
// as before; only destination symlinks are the escape risk and are rejected.
func copyTreeNoFollow(src string, dst string) error {
	if err := rejectExistingSymlink(dst); err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyTreeNoFollow(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	data, err := os.ReadFile(src) // #nosec G304 -- src is a caller-provided seed source path.
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, info.Mode().Perm()) // #nosec G703 -- dst is validated against destination symlinks before copying.
}

// rejectExistingSymlink returns an error when p already exists and is a
// symlink. A nonexistent path is fine.
func rejectExistingSymlink(p string) error {
	info, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to seed through symlinked store path %q", p)
	}
	return nil
}

// chmodTree recursively chmods root, skipping symlink entries to avoid
// changing permissions on targets outside the store.
func chmodTree(root string, mode fs.FileMode) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		return os.Chmod(p, mode) // #nosec G122 -- destination is a runtime-managed store and symlink entries are skipped.
	})
}

// storeFilePath validates a store-relative path against traversal and joins it
// onto the store directory. It rejects any parent component that is a symlink;
// when rejectLeafSymlink is set it also rejects a symlinked leaf, so operations
// that follow the final component (read, write, seed) cannot escape the store.
func storeFilePath(dir string, rel string, rejectLeafSymlink bool) (string, error) {
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
	return target, nil
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
