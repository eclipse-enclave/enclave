// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux

package a2ahub

import (
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// setUserTimeout sets TCP_USER_TIMEOUT so silent loss of a peer closes the
// socket; accepted sockets inherit it from the listener (CR-P1D-3).
func setUserTimeout(c syscall.RawConn, d time.Duration) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		sock := int(fd) // #nosec G115 -- socket descriptors fit in int.
		serr = unix.SetsockoptInt(sock, unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, int(d.Milliseconds()))
	})
	if err != nil {
		return err
	}
	return serr
}
