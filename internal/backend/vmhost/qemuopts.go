// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vmhost

import (
	"fmt"
	"strconv"
	"strings"

	"enclave/internal/backend"
)

// FormatHostfwd renders a published port as a QEMU user-network hostfwd
// rule. A missing host address binds loopback, not all interfaces.
func FormatHostfwd(backendName string, port backend.PortMapping) (string, error) {
	protocol := strings.ToLower(strings.TrimSpace(port.Protocol))
	if protocol != "" && protocol != "tcp" {
		return "", fmt.Errorf("%s backend: only tcp port forwarding is supported", backendName)
	}
	hostPort, err := strconv.Atoi(strings.TrimSpace(port.HostPort))
	if err != nil || hostPort < 1 || hostPort > 65535 {
		return "", fmt.Errorf("%s backend: invalid host port %q", backendName, port.HostPort)
	}
	containerPort, err := strconv.Atoi(strings.TrimSpace(port.ContainerPort))
	if err != nil || containerPort < 1 || containerPort > 65535 {
		return "", fmt.Errorf("%s backend: invalid guest port %q", backendName, port.ContainerPort)
	}
	hostIP := strings.TrimSpace(port.HostIP)
	if hostIP == "" {
		hostIP = "127.0.0.1"
	}
	return fmt.Sprintf("tcp:%s:%d-:%d", hostIP, hostPort, containerPort), nil
}

// RejectOptionComma refuses a value that QEMU would split into further
// suboptions.
func RejectOptionComma(backendName string, label string, value string) error {
	if strings.Contains(value, ",") {
		return fmt.Errorf("%s backend: %s %q contains a comma, which is not supported in QEMU option values", backendName, label, value)
	}
	return nil
}
