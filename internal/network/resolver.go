// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package network

import (
	"fmt"
	"net"
	"strings"
)

// NormalizeResolver returns value as a canonical IPv4 address the gateway can
// forward DNS lookups to. The gateway only uses IPv4 resolvers, and the
// unspecified, broadcast, and multicast addresses cannot answer a query.
func NormalizeResolver(value string) (string, error) {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("resolver must be an IPv4 address, got %q", value)
	}
	v4 := ip.To4()
	if v4.IsUnspecified() || v4.Equal(net.IPv4bcast) || v4.IsMulticast() {
		return "", fmt.Errorf("resolver %s cannot answer DNS queries", v4)
	}
	return v4.String(), nil
}
