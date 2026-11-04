// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !linux

package mitm

import (
	"errors"
	"net"
	"net/netip"
)

// kernelOriginalDestination needs Linux conntrack; the gateway only runs on
// Linux, so other platforms never redirect connections to the proxy.
func kernelOriginalDestination(net.Conn) (netip.AddrPort, error) {
	return netip.AddrPort{}, errors.New("original destination lookup requires Linux")
}
