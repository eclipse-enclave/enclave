// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// ReadRegularFile reads the regular file at path, following a symlink. It
// fails when the file is not regular or holds more than limit bytes, so a
// FIFO, a device, or a huge file cannot hang or exhaust the reader.
func ReadRegularFile(path string, limit int64) ([]byte, error) {
	return readRegularFile(path, limit, os.Stat)
}

// ReadRegularFileNoFollow is ReadRegularFile that also refuses a symlink at
// path.
func ReadRegularFileNoFollow(path string, limit int64) ([]byte, error) {
	return readRegularFile(path, limit, os.Lstat)
}

// readRegularFile checks path before it opens it, so a special file is never
// opened, and checks the opened file again, so the file read is the file
// checked. The open does not block: on Unix a FIFO swapped in between the
// checks fails the second one instead of hanging the open. Windows ignores
// O_NONBLOCK.
func readRegularFile(path string, limit int64, stat func(string) (os.FileInfo, error)) ([]byte, error) {
	before, err := stat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	// #nosec G304 -- callers pass host paths they own or the user named.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("%s changed while it was opened", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return data, nil
}
