// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// WithFileLock runs fn while holding an exclusive lock on path.
func WithFileLock(path string, fn func() error) error {
	if path == "" {
		return fn()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// #nosec G304 -- lock path is controlled by internal callers.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := lockFile(file); err != nil {
		return err
	}
	defer func() { _ = unlockFile(file) }()
	return fn()
}

// FileLease owns an open file carrying an exclusive lock.
type FileLease struct {
	mu        sync.Mutex
	file      *os.File
	completed bool
}

// TryFileLease acquires an exclusive lock without waiting.
func TryFileLease(path string) (*FileLease, bool, error) {
	if path == "" {
		return &FileLease{}, true, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	// #nosec G304 -- lock path is controlled by internal callers.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	acquired, err := tryLockFile(file)
	if err != nil {
		_ = file.Close()
		return nil, false, err
	}
	if !acquired {
		_ = file.Close()
		return nil, false, nil
	}
	return &FileLease{file: file}, true, nil
}

// File returns the open lock file while this process owns the lease.
func (l *FileLease) File() *os.File {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.completed {
		return nil
	}
	return l.file
}

// Release unlocks the lease and closes its file.
func (l *FileLease) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.completed {
		return nil
	}
	l.completed = true
	if l.file == nil {
		return nil
	}
	return errors.Join(unlockFile(l.file), l.file.Close())
}

// Relinquish closes this process's descriptor without unlocking it. A child
// process that inherited the descriptor continues to hold the lease.
func (l *FileLease) Relinquish() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.completed {
		return nil
	}
	l.completed = true
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}

// TryFileLock acquires an exclusive lock without waiting. The returned release
// function owns the open lock file and must be called when acquired is true.
func TryFileLock(path string) (release func() error, acquired bool, err error) {
	lease, acquired, err := TryFileLease(path)
	if err != nil || !acquired {
		return nil, acquired, err
	}
	return lease.Release, true, nil
}
