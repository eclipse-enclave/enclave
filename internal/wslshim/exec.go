// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

import (
	"bytes"
	"os"
	"os/exec"
)

// spawnCommand runs wsl.exe with the console handles inherited.
//
// The standard streams are assigned directly rather than through pipes, so the
// agent running inside the distribution — and `docker attach` under it — sees a
// real terminal. The launcher does no terminal emulation of its own.
func spawnCommand(exe, cmdLine string, env []string) error {
	cmd := command(exe, cmdLine, env)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// captureCommand runs wsl.exe and collects its output. It is used only for the
// preflight round trips, which produce a line or two and need no terminal.
func captureCommand(exe, cmdLine string, env []string) ([]byte, []byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := command(exe, cmdLine, env)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func command(exe, cmdLine string, env []string) *exec.Cmd {
	// #nosec G204
	// The command is always wsl.exe, resolved from PATH by the caller. The
	// user's arguments reach it through the command line built by
	// buildCommandLine, not through a shell.
	cmd := exec.Command(exe)
	cmd.Env = env
	setCommandLine(cmd, cmdLine)
	return cmd
}
