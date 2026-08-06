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
