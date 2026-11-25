// Copyright (C) 2026 TypeFox GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"errors"
	"net"
	"syscall"
)

// HostPortHolder probes a TCP port on the host address it would be published
// on and on one overlapping address. It returns the address the port is held
// on, or "" when the port is free. On macOS a probe fails only while the port
// is held on the probed address itself, so a loopback probe misses a holder
// on the wildcard, and a wildcard probe misses one on loopback, where
// sessions publish by default. A listen error other than "address in use",
// such as for a host IP that is not local, is returned as it is.
func HostPortHolder(hostIP string, port string) (string, error) {
	overlapping := "0.0.0.0"
	if net.ParseIP(hostIP).IsUnspecified() {
		overlapping = DefaultPortHostIP
	}
	for _, ip := range []string{hostIP, overlapping} {
		listener, err := net.Listen("tcp", net.JoinHostPort(ip, port))
		if errors.Is(err, syscall.EADDRINUSE) {
			return ip, nil
		}
		if err != nil {
			return "", err
		}
		_ = listener.Close()
	}
	return "", nil
}
