// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"os"
	"os/exec"

	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/usercmd"
)

// runUserHostCommand execs a host user command, passing stdin/stdout/stderr,
// arguments, and exit code through untouched. The inherited environment is
// augmented with enclave context so scripts can re-invoke the binary and
// locate the project and config directories.
func runUserHostCommand(cmd usercmd.Command, args []string, projectDir, home string) int {
	bin, err := os.Executable()
	if err != nil {
		logx.Warnf("could not resolve enclave binary path; ENCLAVE_BIN will be empty: %v", err)
	}

	// #nosec G204 -- cmd.Path is a user-owned executable the user dropped into
	// ~/.config/enclave/commands/host; running it is the same trust level as a
	// shell alias. The OS resolves the interpreter via execve.
	c := exec.Command(cmd.Path, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = append(os.Environ(),
		model.EnvBin+"="+bin,
		model.EnvProjectRoot+"="+projectDir,
		model.EnvConfigDir+"="+config.HostConfigRootDir(home),
	)

	if err := c.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		logx.Errorf("failed to run user command %q (%s): %v", cmd.Name, cmd.Path, err)
		return 1
	}
	return 0
}

// prepareUserSessionCommand rewrites a parsed session user command into a
// shell-style container run: the script is executed inside a sandboxed session
// container at the fixed neutral path model.UserCommandsContainerDir, with the
// discovered arguments passed verbatim. It returns the read-only mount that
// exposes the host session command tree in-container.
//
// The script is exec'd directly (via `bash -c 'exec "$@"'`) rather than
// interpreted by bash, so its shebang is honored via execve — matching the
// opaque-executable contract used for host commands. enclave's own session
// flags (parsed before the command name) already live in parsed.Options and
// flow through the normal run pipeline unchanged.
func prepareUserSessionCommand(parsed *cli.Result, home string) *model.UserCommandMount {
	containerPath := model.UserCommandsContainerDir + "/" + parsed.UserCommand.Name

	parsed.Action = "shell"
	parsed.Options.Shell = true
	// bash -c uses the argument after the script as $0; the rest become $1..
	// for `exec "$@"`, which execve-replaces the shell with the user script so
	// the script's shebang (any interpreter) is honored.
	cmdArgs := []string{"-c", `exec "$@"`, parsed.UserCommand.Name, containerPath}
	cmdArgs = append(cmdArgs, parsed.UserCommandArgs...)
	parsed.Options.CmdArgs = cmdArgs

	return &model.UserCommandMount{
		HostDir:       config.HostCommandsSessionDir(home),
		ContainerPath: model.UserCommandsContainerDir,
	}
}
