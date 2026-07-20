// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build darwin

package logx

import "golang.org/x/sys/unix"

func terminalIsRaw(fd uintptr) bool {
	termios, err := unix.IoctlGetTermios(int(fd), unix.TIOCGETA) // #nosec G115 -- file descriptor from Fd() fits in int on all supported platforms.
	if err != nil {
		return false
	}
	return termios.Oflag&unix.OPOST == 0
}
