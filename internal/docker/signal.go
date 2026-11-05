// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"fmt"
	"strings"
)

// SignalReload is the signal enclave containers treat as a reload request.
const SignalReload = "HUP"

// SignalContainer delivers signal to the main process of a running
// container. signal is a name with or without the SIG prefix (HUP, SIGHUP,
// RTMIN+3) or a number; IsContainerNotRunning classifies a stopped target.
func SignalContainer(ctx context.Context, container, signal string) error {
	if strings.TrimSpace(container) == "" {
		return fmt.Errorf("signal requires a container")
	}
	name, err := normalizeSignal(signal)
	if err != nil {
		return err
	}
	return ContainerSignal(ctx, container, name)
}

// Both engines accept names without the SIG prefix; the check keeps flag-like
// values away from the CLI.
func normalizeSignal(signal string) (string, error) {
	name := strings.ToUpper(strings.TrimSpace(signal))
	name = strings.TrimPrefix(name, "SIG")
	if name == "" {
		return "", fmt.Errorf("invalid signal %q", signal)
	}
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '+' || r == '-') && i > 0:
		default:
			return "", fmt.Errorf("invalid signal %q", signal)
		}
	}
	return name, nil
}

// IsContainerNotRunning reports whether a signal failed because the container
// exists but is not running.
func IsContainerNotRunning(err error) bool {
	s, ok := cliStderr(err)
	if !ok {
		return false
	}
	return strings.Contains(s, "is not running") || strings.Contains(s, "can only kill running containers")
}
