// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package mcp

import (
	"os"
	"syscall"
)

// lockPath takes an exclusive flock so the MCP server and the CLI fallback serialize journal writes.
func lockPath(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- journal directory chosen by the host.
	if err != nil {
		return nil, err
	}
	// #nosec G115 -- file descriptor from os.File.Fd() fits in int on supported Unix platforms.
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) // #nosec G115
		_ = file.Close()
	}, nil
}
