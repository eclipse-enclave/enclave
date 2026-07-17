// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
)

// AttachInteractive attaches to a running container's stdio. The docker CLI
// handles raw-mode, terminal resize, and the detach key sequence. It returns an
// error only when the attach itself fails (for example the container does not
// exist); a normal detach or container exit returns nil.
func AttachInteractive(ctx context.Context, containerNameOrID string, detachKeys string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	args := []string{"attach"}
	if strings.TrimSpace(detachKeys) != "" {
		args = append(args, "--detach-keys", detachKeys)
	}
	args = append(args, containerNameOrID)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204 -- args are fixed flags plus a caller-provided container reference, passed without a shell.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	err := cmd.Run()
	if err == nil {
		return nil
	}
	// A clean detach, a Ctrl-C (SIGINT → non-zero container exit), and a normal
	// container exit are all benign and must return nil, matching the SDK path
	// which ignored the container's exit code once attached. Only surface cases
	// where docker could not establish the attach at all — those are reported on
	// stderr by the daemon/CLI.
	if _, ok := commandExitCode(err); ok {
		if attachConnectFailed(stderr.String()) {
			return &cliError{args: args, stderr: strings.TrimSpace(stderr.String()), err: err}
		}
		return nil
	}
	return err
}

// attachConnectFailed reports whether docker's stderr indicates the attach
// could not be established — a missing or stopped container, invalid detach
// keys, or a daemon/permission error — as opposed to a normal detach or the
// attached container exiting (which produce no docker-level error output).
func attachConnectFailed(stderr string) bool {
	s := strings.ToLower(strings.TrimSpace(stderr))
	if s == "" {
		return false
	}
	for _, marker := range []string{
		"no such container",
		"is not running",
		"cannot attach",
		"stopped container",
		"invalid detach",
		"detach keys",
		"cannot connect to the docker daemon",
		"permission denied",
		"error response from daemon",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	// Docker CLI errors surface as a leading "Error..." line; container output
	// does not reach this (docker-owned) stderr stream for TTY sessions.
	firstLine := s
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		firstLine = s[:idx]
	}
	return strings.HasPrefix(firstLine, "error")
}
