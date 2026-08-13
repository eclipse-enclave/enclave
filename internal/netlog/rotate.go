// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"fmt"
	"os"
	"strings"
)

// RotatedSuffix marks the single retained previous generation of a log.
const RotatedSuffix = ".1"

// RotatedPath is the previous generation of the given log path.
func RotatedPath(path string) string {
	return path + RotatedSuffix
}

// ReadPaths returns the log files to read for one log, oldest first, skipping
// the ones that do not exist. Reading the rotated generation before the live
// one makes a rotation boundary invisible to the caller.
func ReadPaths(path string) []string {
	var paths []string
	for _, candidate := range []string{RotatedPath(path), path} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			paths = append(paths, candidate)
		}
	}
	return paths
}

// RotateIfLarger renames path to path.1 when it exceeds maxBytes, replacing any
// previous generation. maxBytes of zero or less disables rotation. The first
// return reports whether a rotation happened.
//
// Rotating rather than truncating preserves history across sessions, which
// after-the-fact audit depends on. Nothing rotates mid-session: a single
// long-running session can grow past the cap.
func RotateIfLarger(path string, maxBytes int64) (bool, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || maxBytes <= 0 {
		return false, nil
	}
	info, err := os.Stat(trimmed)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() || info.Size() <= maxBytes {
		return false, nil
	}
	if err := os.Rename(trimmed, RotatedPath(trimmed)); err != nil {
		return false, fmt.Errorf("rotate %s: %w", trimmed, err)
	}
	return true, nil
}
