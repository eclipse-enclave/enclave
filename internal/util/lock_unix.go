// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package util

import (
	"os"
	"syscall"
)

func lockFile(file *os.File) error {
	// #nosec G115 -- file descriptor from os.File.Fd() fits in int on supported Unix platforms.
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

func tryLockFile(file *os.File) (bool, error) {
	// #nosec G115 -- file descriptor from os.File.Fd() fits in int on supported Unix platforms.
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
		return false, nil
	}
	return err == nil, err
}

func unlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN) // #nosec G115
}
