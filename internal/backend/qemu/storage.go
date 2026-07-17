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
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/model"
	"enclave/internal/util"
)

// StoreManager realizes persistent stores on the shared hoststore layout, so a
// qemu session reads and writes the exact same auth/config/env directories as
// a Docker session for the same tool and project. Host-side IO stays hardened
// against guest-planted symlinks because store trees are guest-writable via
// 9p.
type StoreManager struct {
	host model.Host
}

func newStoreManager(host model.Host) *StoreManager {
	return &StoreManager{host: host}
}

// Ensure creates the backing directory. Like the Docker backend, owner is a
// no-op: qemu runs as the invoking user and the guest agent runs with
// Host.UID/GID over 9p (security_model=none), so store content is already
// correctly owned. Chowning the shared tree would also fail on foreign-owned
// entries other backends leave behind (e.g. root-owned Docker bind-mount
// point directories inside the config store).
func (s *StoreManager) Ensure(_ context.Context, key backend.StoreKey, kind backend.StoreKind, _ string) error {
	root, err := s.storePath(key, kind)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create store %s: %w", root, err)
	}
	return nil
}

func (s *StoreManager) ReadFile(_ context.Context, key backend.StoreKey, kind backend.StoreKind, rel string) ([]byte, error) {
	target, err := s.storeRelPath(key, kind, rel)
	if err != nil {
		return nil, err
	}
	// storeRelPath rejected symlinked parents; refuse a symlinked final
	// component too, so a guest-planted link cannot redirect the read to a host
	// file outside the store.
	info, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to read symlinked store path %q", rel)
	}
	data, err := os.ReadFile(target) // #nosec G304 -- path validated no-follow under the store root.
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (s *StoreManager) WriteFile(ctx context.Context, key backend.StoreKey, kind backend.StoreKind, rel string, data []byte, mode fs.FileMode) error {
	return s.writeFile(ctx, key, kind, rel, data, mode)
}

// WriteFileOwned writes a store file. Ownership is inherited from the invoking
// user, so owner is a no-op and this behaves exactly like WriteFile.
func (s *StoreManager) WriteFileOwned(ctx context.Context, key backend.StoreKey, kind backend.StoreKind, rel string, data []byte, mode fs.FileMode, _ string) error {
	return s.writeFile(ctx, key, kind, rel, data, mode)
}

func (s *StoreManager) writeFile(ctx context.Context, key backend.StoreKey, kind backend.StoreKind, rel string, data []byte, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	if err := s.Ensure(ctx, key, kind, ""); err != nil {
		return err
	}
	target, err := s.storeRelPath(key, kind, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create store file parent: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp store file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath) // #nosec G703 -- tmpPath comes from os.CreateTemp in the validated store directory.
		return fmt.Errorf("write temp store file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath) // #nosec G703 -- tmpPath comes from os.CreateTemp in the validated store directory.
		return fmt.Errorf("close temp store file: %w", err)
	}
	if err := os.Chmod(tmpPath, mode.Perm()); err != nil { // #nosec G703 -- tmpPath comes from os.CreateTemp in the validated store directory.
		_ = os.Remove(tmpPath) // #nosec G703 -- tmpPath comes from os.CreateTemp in the validated store directory.
		return fmt.Errorf("chmod temp store file: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil { // #nosec G703 -- target is constrained to the resolved store root.
		_ = os.Remove(tmpPath) // #nosec G703 -- tmpPath comes from os.CreateTemp in the validated store directory.
		return fmt.Errorf("replace store file: %w", err)
	}
	return nil
}

func (s *StoreManager) Seed(ctx context.Context, key backend.StoreKey, kind backend.StoreKind, items []backend.SeedItem) error {
	if len(items) == 0 {
		return nil
	}
	if err := s.Ensure(ctx, key, kind, ""); err != nil {
		return err
	}
	for _, item := range items {
		if strings.TrimSpace(item.HostPath) == "" || strings.TrimSpace(item.StoreRel) == "" {
			continue
		}
		dst, err := s.storeRelPath(key, kind, item.StoreRel)
		if err != nil {
			return err
		}
		if err := copyPath(item.HostPath, dst, item.Mode); err != nil {
			return err
		}
	}
	return nil
}

func (s *StoreManager) RemovePath(_ context.Context, key backend.StoreKey, kind backend.StoreKind, rel string) error {
	target, err := s.storeRelPath(key, kind, rel)
	if err != nil {
		return err
	}
	return os.RemoveAll(target)
}

func (s *StoreManager) Remove(_ context.Context, key backend.StoreKey, kind backend.StoreKind) error {
	root, err := s.storePath(key, kind)
	if err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func (s *StoreManager) StoreExists(_ context.Context, key backend.StoreKey, kind backend.StoreKind) (bool, error) {
	root, err := s.storePath(key, kind)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(root)
	if err == nil {
		return info.IsDir(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// WithStoreLock serializes cross-process access to one store. The lock file
// is derived from the shared store directory, so qemu sessions, Docker
// sessions, and Docker-private store mechanics all contend on the same lock.
func (s *StoreManager) WithStoreLock(_ context.Context, key backend.StoreKey, kind backend.StoreKind, fn func() error) error {
	if fn == nil {
		return nil
	}
	dir, err := s.storePath(key, kind)
	if err != nil {
		return err
	}
	return hoststore.WithLock(s.host.Home, dir, fn)
}

// storePath resolves the shared host directory backing a store, guarding the
// enclave-owned path chain against planted symlinks (see hoststore.ResolveDir).
func (s *StoreManager) storePath(key backend.StoreKey, kind backend.StoreKind) (string, error) {
	return hoststore.ResolveDir(s.host.Home, key, kind)
}

func (s *StoreManager) storeRelPath(key backend.StoreKey, kind backend.StoreKind, rel string) (string, error) {
	root, err := s.storePath(key, kind)
	if err != nil {
		return "", err
	}
	cleaned, err := cleanStoreRel(rel)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(cleaned))
	if !util.PathWithin(root, target) {
		return "", fmt.Errorf("invalid store path %q", rel)
	}
	if err := verifyStoreParentsNoFollow(root, cleaned); err != nil {
		return "", err
	}
	return target, nil
}

// verifyStoreParentsNoFollow ensures no existing parent directory component of
// cleanedRel under root is a symlink. Store trees are guest-writable over 9p,
// so a guest-planted symlink on an intermediate component (e.g. "agent" in
// "agent/auth.json" pointing at a host directory) must not redirect host-side
// reads, writes, copies, or removals outside the store. Missing trailing
// components are allowed (nothing to follow yet). cleanedRel must already be
// cleanStoreRel-validated. Safe without TOCTOU because the guest is powered off
// during all host-side store access.
func verifyStoreParentsNoFollow(root string, cleanedRel string) error {
	parts := strings.Split(cleanedRel, "/")
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // remaining components do not exist; nothing to follow
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to traverse symlinked store path component %q", part)
		}
		if !info.IsDir() {
			return fmt.Errorf("store path component %q is not a directory", part)
		}
	}
	return nil
}

func (s *StoreManager) MountSource(key backend.StoreKey, kind backend.StoreKind) (string, error) {
	root, err := s.storePath(key, kind)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return root, nil
}

func cleanStoreRel(rel string) (string, error) {
	value := strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	if value == "" {
		return "", fmt.Errorf("store path is empty")
	}
	cleaned := filepath.ToSlash(filepath.Clean(value))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("invalid store path %q", rel)
	}
	return cleaned, nil
}

// copyPath copies src into dst WITHOUT following symlinks. Store contents are
// guest-writable over 9p, so dereferencing a guest-planted symlink here would
// read arbitrary host-readable files and copy their contents back into the
// store (and thus into the next guest). Symlinks are recreated as links, which
// also preserves legitimate store links (e.g. .credentials.json -> shared auth).
// Directories recurse; regular files are copied by content.
func copyPath(src string, dst string, mode fs.FileMode) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return recreateSymlink(src, dst)
	case info.IsDir():
		if err := ensureCopyDir(dst, 0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyPath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()), mode); err != nil {
				return err
			}
		}
		return nil
	default:
		return copyFile(src, dst, firstNonZeroMode(mode, info.Mode().Perm()))
	}
}

// recreateSymlink copies the symlink node itself (its target string), never the
// content the link points at.
func recreateSymlink(src string, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := removeCopyDestination(dst); err != nil {
		return err
	}
	return os.Symlink(target, dst)
}

func ensureCopyDir(dst string, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o700
	}
	info, err := os.Lstat(dst)
	if err == nil {
		if info.Mode()&os.ModeSymlink == 0 && info.IsDir() {
			return nil
		}
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dst, mode.Perm()); err != nil {
		if !os.IsExist(err) {
			return err
		}
		info, statErr := os.Lstat(dst)
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("copy destination %s exists and is not a directory", dst)
		}
	}
	return nil
}

func removeCopyDestination(dst string) error {
	if _, err := os.Lstat(dst); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.RemoveAll(dst)
}

func copyFile(src string, dst string, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	in, err := os.Open(src) // #nosec G304 -- caller validates source.
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := removeCopyDestination(dst); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode.Perm()) // #nosec G304 -- dst is constrained by caller.
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode.Perm())
}

func firstNonZeroMode(primary fs.FileMode, fallback fs.FileMode) fs.FileMode {
	if primary != 0 {
		return primary.Perm()
	}
	return fallback.Perm()
}
