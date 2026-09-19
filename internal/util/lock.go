// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"os"
	"path/filepath"
	"sync"
)

// AcquireFileLock acquires an exclusive lock on path and returns an idempotent
// release function. onWait is called when another process currently holds the
// lock, immediately before blocking for it.
func AcquireFileLock(path string, onWait func()) (release func(), err error) {
	if path == "" {
		return func() {}, nil
	}
	file, locked, err := openAndTryLock(path)
	if err != nil {
		return nil, err
	}
	if !locked {
		if onWait != nil {
			onWait()
		}
		if err := lockFile(file); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return lockReleaser(file), nil
}

// TryAcquireFileLock is AcquireFileLock without waiting: ok is false, and
// release nil, when another process holds the lock.
func TryAcquireFileLock(path string) (release func(), ok bool, err error) {
	file, locked, err := openAndTryLock(path)
	if err != nil || !locked {
		if file != nil {
			_ = file.Close()
		}
		return nil, false, err
	}
	return lockReleaser(file), true, nil
}

func openAndTryLock(path string) (*os.File, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	// #nosec G304 -- lock path is controlled by internal callers.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	locked, err := tryLockFile(file)
	if err != nil {
		_ = file.Close()
		return nil, false, err
	}
	return file, locked, nil
}

func lockReleaser(file *os.File) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unlockFile(file)
			_ = file.Close()
		})
	}
}

// WithFileLock runs fn while holding an exclusive lock on path.
func WithFileLock(path string, fn func() error) error {
	release, err := AcquireFileLock(path, nil)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}
