// Copyright (C) 2026 TypeFox GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"net"
	"strconv"
	"testing"
)

func TestHostPortHolder(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)

	// Published on the wildcard, the port is still found held on loopback.
	for _, hostIP := range []string{DefaultPortHostIP, "0.0.0.0"} {
		holder, err := HostPortHolder(hostIP, port)
		if err != nil || holder == "" {
			t.Errorf("HostPortHolder(%s) = %q, %v while the port is held", hostIP, holder, err)
		}
	}

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if holder, err := HostPortHolder(DefaultPortHostIP, port); err != nil || holder != "" {
		t.Errorf("HostPortHolder = %q, %v for a free port", holder, err)
	}
}
