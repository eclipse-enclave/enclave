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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// #nosec G304 -- lock path is controlled by internal callers.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	locked, err := tryLockFile(file)
	if err != nil {
		_ = file.Close()
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
	var once sync.Once
	release = func() {
		once.Do(func() {
			_ = unlockFile(file)
			_ = file.Close()
		})
	}
	return release, nil
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
