// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/util"
)

// RotatedSuffix marks the single retained previous generation of a log.
const RotatedSuffix = ".1"

// MaxLogBytes is the size above which a log is rotated at session start. Two
// generations of it is the disk a project's network log may occupy.
const MaxLogBytes = 32 * 1024 * 1024

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
		if util.FileExists(candidate) {
			paths = append(paths, candidate)
		}
	}
	return paths
}

// RotateIfLarger copies path to path.1 and truncates path when it exceeds
// maxBytes, replacing any previous generation. maxBytes of zero or less disables
// rotation. The first return reports whether a rotation happened.
//
// Rotating rather than discarding preserves history across sessions, which
// after-the-fact audit depends on. Copying and truncating rather than renaming
// keeps the inode: the gateway bind-mounts the log as a single file, so a rename
// would leave a concurrently running session appending to the rotated
// generation, invisible to `network log -f`. Writers open the log with O_APPEND
// and so continue at the new end. An event written between the copy and the
// truncate is lost, which is the accepted cost of not orphaning a live writer.
func RotateIfLarger(path string, maxBytes int64) (bool, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || maxBytes <= 0 {
		return false, nil
	}
	// Sessions of one project and tool start independently and rotate the same
	// file. The lock spans the size check, the copy and the truncate, so a second
	// session cannot copy the file the first has already emptied over the
	// generation the first just wrote.
	var rotated bool
	err := util.WithFileLock(trimmed+".lock", func() (err error) {
		rotated, err = rotateLocked(trimmed, maxBytes)
		return err
	})
	return rotated, err
}

func rotateLocked(path string, maxBytes int64) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() || info.Size() <= maxBytes {
		return false, nil
	}
	if err := copyLog(path, RotatedPath(path)); err != nil {
		return false, fmt.Errorf("rotate %s: %w", path, err)
	}
	if err := os.Truncate(path, 0); err != nil {
		return false, fmt.Errorf("truncate %s: %w", path, err)
	}
	return true, nil
}

// copyLog replaces dst with the contents of src. The copy lands in a hidden
// temporary file first, so a reader of dst never sees a half-written generation
// and nothing globbing the log name picks the staging file up.
func copyLog(src string, dst string) error {
	source, err := os.Open(src) // #nosec G304 -- path is resolved from enclave's own state directory.
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	target, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*")
	if err != nil {
		return err
	}
	staged := target.Name()
	defer func() { _ = os.Remove(staged) }()

	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		return err
	}
	// The live log is truncated straight after this returns, so the copy has to
	// reach disk first or a crash in between would lose the history entirely.
	if err := target.Sync(); err != nil {
		_ = target.Close()
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	return os.Rename(staged, dst) // #nosec G703 -- dst is derived from enclave's own state directory; staged is a sibling temp file.
}
