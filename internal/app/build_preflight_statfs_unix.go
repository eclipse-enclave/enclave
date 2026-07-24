// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package app

import (
	"fmt"
	"syscall"
)

func availableBytesAtPath(path string) (uint64, error) {
	resolved, err := existingPathForStatfs(path)
	if err != nil {
		return 0, err
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(resolved, &stat); err != nil {
		return 0, fmt.Errorf("stat filesystem for %s: %w", resolved, err)
	}

	// Statfs reports available blocks in units of Bsize on Linux and macOS.
	// The Bsize type differs across platforms, so reject negative values before
	// converting it to uint64.
	blockSize := stat.Bsize
	if blockSize <= 0 {
		return 0, fmt.Errorf("stat filesystem for %s: invalid block size", resolved)
	}
	availableBlocks := int64(stat.Bavail) // #nosec G115 -- high-bit uint64 values become negative and are rejected below.
	if availableBlocks < 0 {
		return 0, fmt.Errorf("stat filesystem for %s: invalid available block count", resolved)
	}
	return uint64(availableBlocks) * uint64(blockSize), nil
}
