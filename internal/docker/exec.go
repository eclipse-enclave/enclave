// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"os"
	"os/exec"
)

// ExecInteractive runs cmd inside a running container attached to the current
// terminal. The docker CLI handles raw-mode and terminal resize. A non-zero
// command exit is returned as an *ExitError.
func ExecInteractive(ctx context.Context, containerID string, cmd []string, user string, tty bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	args := []string{"exec", "--interactive"}
	if tty {
		args = append(args, "--tty")
	}
	if user != "" {
		args = append(args, "--user", user)
	}
	args = append(args, containerID)
	args = append(args, cmd...)

	command := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204 -- args are fixed flags plus caller-provided command, passed without a shell.
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	err := command.Run()
	if err == nil {
		return nil
	}
	if code, ok := commandExitCode(err); ok {
		return &ExitError{Code: code}
	}
	return err
}

// ExecCapture runs cmd inside a running container without a TTY and returns
// its stdout verbatim. Unlike capture it does not trim the output: callers
// capture terminal screens where leading/trailing blank lines are meaningful.
// An empty user runs as the container's default exec user. On failure it
// returns stdout so far and a *cliError carrying stderr and the exit code.
func ExecCapture(ctx context.Context, containerID string, cmd []string, user string) (string, error) {
	args := []string{"exec"}
	if user != "" {
		args = append(args, "--user", user)
	}
	args = append(args, containerID)
	args = append(args, cmd...)
	return captureCmd(ctx, false, args...)
}
