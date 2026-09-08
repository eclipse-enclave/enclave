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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

// Lease is a lifetime store lease that can be handed to a child process.
type Lease struct {
	*util.FileLease
}

const (
	leaseHandoffTimeout = 5 * time.Second
	inheritedLeaseFD    = 3
	leaseReadyFD        = 4
)

// TryLease acquires a non-blocking lifetime lease for a store. Lease locks are
// separate from the short-lived operation locks used by WithLock.
func TryLease(hostHome string, dir string) (*Lease, bool, error) {
	if hostHome == "" || dir == "" {
		return nil, false, fmt.Errorf("store lease requires host home and directory")
	}
	lockPath := config.HostLockPath(hostHome, "store-lease-"+util.HashString(dir)+".lock")
	if lockPath == "" {
		return nil, false, fmt.Errorf("resolve store lease path")
	}
	lease, acquired, err := util.TryFileLease(lockPath)
	if err != nil || !acquired {
		return nil, acquired, err
	}
	return &Lease{FileLease: lease}, true, nil
}

// Handoff starts a child process with the lease descriptor inherited as fd 3.
// The child confirms readiness through fd 4 before this process relinquishes
// its descriptor, so the lock remains continuously held across the handoff.
func (l *Lease) Handoff(command string, args ...string) error {
	if l == nil || l.FileLease == nil || l.File() == nil {
		return fmt.Errorf("store lease is not active")
	}
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("store lease monitor command is empty")
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create store lease monitor pipe: %w", err)
	}
	defer func() { _ = readyReader.Close() }()

	cmd := exec.Command(command, args...) // #nosec G204 -- command and args are selected by internal callers.
	cmd.ExtraFiles = []*os.File{l.File(), readyWriter}
	if err := cmd.Start(); err != nil {
		_ = readyWriter.Close()
		return fmt.Errorf("start store lease monitor: %w", err)
	}
	_ = readyWriter.Close()

	ready := make(chan error, 1)
	go func() {
		var signal [1]byte
		_, readErr := io.ReadFull(readyReader, signal[:])
		if readErr == nil && signal[0] != 1 {
			readErr = fmt.Errorf("invalid readiness signal")
		}
		ready <- readErr
	}()

	var readyErr error
	select {
	case readyErr = <-ready:
	case <-time.After(leaseHandoffTimeout):
		readyErr = fmt.Errorf("timed out after %s", leaseHandoffTimeout)
		_ = readyReader.Close()
	}
	if readyErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("initialize store lease monitor: %w", readyErr)
	}
	if err := cmd.Process.Release(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("release store lease monitor process: %w", err)
	}
	if err := l.Relinquish(); err != nil {
		return fmt.Errorf("relinquish store lease to monitor: %w", err)
	}
	return nil
}

// AcceptInheritedLease validates the inherited lease descriptor, acknowledges
// the handoff through fd 4, and returns the descriptor for the monitor to keep
// open until its work is complete.
func AcceptInheritedLease() (*os.File, error) {
	leaseFile := os.NewFile(inheritedLeaseFD, "config-store-lease")
	readyFile := os.NewFile(leaseReadyFD, "config-store-lease-ready")
	if leaseFile == nil || readyFile == nil {
		if leaseFile != nil {
			_ = leaseFile.Close()
		}
		if readyFile != nil {
			_ = readyFile.Close()
		}
		return nil, fmt.Errorf("store lease monitor descriptors are unavailable")
	}
	if _, err := leaseFile.Stat(); err != nil {
		_ = leaseFile.Close()
		_ = readyFile.Close()
		return nil, fmt.Errorf("inspect inherited store lease: %w", err)
	}
	if _, err := readyFile.Write([]byte{1}); err != nil {
		_ = leaseFile.Close()
		_ = readyFile.Close()
		return nil, fmt.Errorf("acknowledge inherited store lease: %w", err)
	}
	if err := readyFile.Close(); err != nil {
		_ = leaseFile.Close()
		return nil, fmt.Errorf("close store lease readiness pipe: %w", err)
	}
	return leaseFile, nil
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
