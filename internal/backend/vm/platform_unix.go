// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package vm

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// fileID identifies the file behind info across processes: a session record
// carries it from the start of a session to its finalization.
func fileID(info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
}

// ownedPrivateDir reports whether path is a directory, not a link to one,
// that belongs to this user and that nobody else can enter.
func ownedPrivateDir(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

// ownSession starts a helper in a session of its own. It then gets neither
// the terminal's interrupt nor its hangup, and keeps serving a detached VM
// after the enclave process that started it has exited.
func ownSession(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
