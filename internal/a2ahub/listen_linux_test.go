// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux

package a2ahub

import (
	"context"
	"net"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestListenerSetsUserTimeoutOnAcceptedSockets(t *testing.T) {
	ln, err := Listen(context.Background(), 0, DefaultUserTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	client, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	var conn net.Conn
	select {
	case conn = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("no connection accepted")
	}
	if conn == nil {
		t.Fatal("accept failed")
	}
	defer func() { _ = conn.Close() }()
	raw, err := conn.(syscall.Conn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var got int
	var gerr error
	if err := raw.Control(func(fd uintptr) {
		sock := int(fd) // #nosec G115 -- socket descriptors fit in int.
		got, gerr = unix.GetsockoptInt(sock, unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT)
	}); err != nil || gerr != nil {
		t.Fatal(err, gerr)
	}
	if got != int(DefaultUserTimeout.Milliseconds()) {
		t.Fatalf("TCP_USER_TIMEOUT %d ms on the accepted socket", got)
	}
}
