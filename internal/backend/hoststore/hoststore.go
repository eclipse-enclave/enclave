// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package hoststore maps neutral persistent-store identities (backend.StoreKey
// plus backend.StoreKind) to the host directories that back them, and provides
// the cross-process lock shared by every store consumer. All isolation
// backends (Docker bind mounts, QEMU 9p shares) realize stores from this
// single layout, so auth, env, and config state stay shared between containers
// and microVMs.
package hoststore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/util"
)

// defaultStoreKey is the config-store key used when a session has no
// worktree/session suffix. It mirrors the config-generated/<key> convention.
const defaultStoreKey = "default"

// DirFor maps a neutral store identity to its host directory. Every store key
// field consumed as a filesystem path segment (owner, project hash,
// config-store key) is validated as a single safe segment so a malformed key
// can never escape the state-root layout.
func DirFor(home string, key backend.StoreKey, kind backend.StoreKind) (string, error) {
	owner, err := validateStoreSegment(key.Owner)
	if err != nil {
		return "", fmt.Errorf("store owner: %w", err)
	}
	switch kind {
	case backend.StoreKindAuth:
		// Suffix carries the named identity slug (--auth-name); empty selects
		// the default identity.
		identity := ""
		if strings.TrimSpace(key.Suffix) != "" {
			identity, err = validateStoreSegment(key.Suffix)
			if err != nil {
				return "", fmt.Errorf("store auth identity: %w", err)
			}
		}
		return config.HostStoreAuthDir(home, owner, identity), nil
	case backend.StoreKindFeatureAuth:
		return config.HostStoreFeatureAuthDir(home, owner), nil
	case backend.StoreKindEnv:
		hash, err := validateStoreSegment(key.ProjectHash)
		if err != nil {
			return "", fmt.Errorf("store project hash: %w", err)
		}
		return config.HostStoreEnvDir(home, owner, hash), nil
	default:
		hash, err := validateStoreSegment(key.ProjectHash)
		if err != nil {
			return "", fmt.Errorf("store project hash: %w", err)
		}
		storeKey, err := validateStoreSegment(configStoreKey(key.Suffix))
		if err != nil {
			return "", fmt.Errorf("store key: %w", err)
		}
		return config.HostStoreConfigDir(home, owner, hash, storeKey), nil
	}
}

// ResolveDir resolves the host directory backing a store and rejects any
// symlink in the enclave-owned chain from the state root down to the store
// directory itself: a planted symlinked store root would otherwise let every
// operation escape. The state root and anything above it may be a legitimate
// symlink (e.g. XDG dirs), so the walk starts below it. Every isolation
// backend resolves shared stores through this single guard.
func ResolveDir(home string, key backend.StoreKey, kind backend.StoreKind) (string, error) {
	dir, err := DirFor(home, key, kind)
	if err != nil {
		return "", err
	}
	if err := EnsureNoSymlinkChain(config.HostStateRootDir(home), dir, true); err != nil {
		return "", err
	}
	return dir, nil
}

// Dir reports the host directory backing a store, or an empty string when the
// store key is incomplete or malformed. It exists for informational output
// (e.g. the info command); session code must not construct or pass around
// store paths.
func Dir(home string, key backend.StoreKey, kind backend.StoreKind) string {
	dir, err := DirFor(home, key, kind)
	if err != nil {
		return ""
	}
	return dir
}

// WithLock serializes cross-process access to a host-directory backed store
// via a file lock keyed by the store directory. Every backend and every
// backend-private store mechanic derives its lock from the same store
// directory, so concurrent sessions can never desync — regardless of which
// backend they run under.
func WithLock(hostHome string, dir string, fn func() error) error {
	if hostHome == "" || dir == "" {
		return fn()
	}
	lockPath := config.HostLockPath(hostHome, "store-"+util.HashString(dir)+".lock")
	if lockPath == "" {
		return fn()
	}
	return util.WithFileLock(lockPath, fn)
}

// EnsureNoSymlinkChain rejects a target whose path from root traverses a
// symlink. Components strictly below root are always checked; the final
// component is checked only when includeLeaf is set. It defends against a
// symlinked directory (or store root) redirecting host-side operations outside
// the intended tree. Nonexistent components are fine: our own code creates
// them as real directories.
func EnsureNoSymlinkChain(root string, target string, includeLeaf bool) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("store path %q escapes %q", target, root)
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if !includeLeaf {
		parts = parts[:len(parts)-1]
	}
	current := root
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to traverse symlinked store path component %q", current)
		}
	}
	return nil
}

func configStoreKey(suffix string) string {
	if trimmed := strings.TrimSpace(suffix); trimmed != "" {
		return trimmed
	}
	return defaultStoreKey
}

// validateStoreSegment rejects any value that is not a single, safe path
// segment (empty, ".", "..", or containing a path separator), so store keys
// cannot escape the intended directory layout.
func validateStoreSegment(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("empty store path segment")
	}
	if trimmed == "." || trimmed == ".." || strings.ContainsAny(trimmed, `/\`) || trimmed != filepath.Base(trimmed) {
		return "", fmt.Errorf("invalid store path segment %q", value)
	}
	return trimmed, nil
}
