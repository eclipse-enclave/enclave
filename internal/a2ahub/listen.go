// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"context"
	"net"
	"strconv"
	"syscall"
	"time"
)

// Listen opens the IPv4 hub listener with TCP_USER_TIMEOUT set.
func Listen(ctx context.Context, port int, userTimeout time.Duration) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		return setUserTimeout(c, userTimeout)
	}}
	return lc.Listen(ctx, "tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
}

func newDialer(timeout, userTimeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout: timeout,
		Control: func(_, _ string, c syscall.RawConn) error {
			return setUserTimeout(c, userTimeout)
		},
	}
}
