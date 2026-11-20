// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package vm

import (
	"os"
	"syscall"
)

// dupFile duplicates a file as a child process inherits it: a second
// descriptor of the same open file, and so of the same lock.
func dupFile(file *os.File) *os.File {
	if file == nil {
		return nil
	}
	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		return nil
	}
	return os.NewFile(uintptr(fd), file.Name())
}
