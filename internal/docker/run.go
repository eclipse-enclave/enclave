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
	"time"
)

// classifyRunError turns a failed `docker run` into the error type callers
// expect: an *ExitError for a non-zero container exit, or a *cliError when
// docker itself could not start the container (exit code 125) or failed for a
// non-exit reason.
func classifyRunError(args []string, err error, stderr string) error {
	if err == nil {
		return nil
	}
	stderr = strings.TrimSpace(stderr)
	code, ok := commandExitCode(err)
	if !ok || code == dockerExitCodeUnableToStart {
		return &cliError{args: args, code: code, stderr: stderr, err: err}
	}
	return &ExitError{Code: code, Stderr: stderr}
}

// Run runs a container to completion, discarding its output, and returns an
// *ExitError when the container exits non-zero.
func Run(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	args := buildRunArgs(config, hostConfig, name, runMode{})
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204 -- args built from caller config, passed without a shell.
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	return classifyRunError(args, cmd.Run(), stderr.String())
}

// RunWithIO runs a container wired to the supplied streams (no TTY) and returns
// an *ExitError when the container exits non-zero.
func RunWithIO(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string, in io.Reader, out io.Writer, errOut io.Writer) error {
	return runWithIO(ctx, config, hostConfig, name, in, out, errOut, false)
}

// RunWithIOAndTTY runs a container wired to the supplied streams with a TTY
// allocated and returns an *ExitError when the container exits non-zero.
func RunWithIOAndTTY(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string, in io.Reader, out io.Writer, errOut io.Writer) error {
	return runWithIO(ctx, config, hostConfig, name, in, out, errOut, true)
}

func runWithIO(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string, in io.Reader, out io.Writer, errOut io.Writer, tty bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	mode := runMode{Interactive: in != nil, TTY: tty}
	args := buildRunArgs(config, hostConfig, name, mode)
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = errOut
	return classifyRunError(args, cmd.Run(), "")
}

// RunCapture runs a container and returns its trimmed stdout, surfacing stderr
// on a non-zero exit via the returned *ExitError.
func RunCapture(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	args := buildRunArgs(config, hostConfig, name, runMode{})
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", classifyRunError(args, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// RunInteractive runs a container attached to the current terminal. The docker
// CLI handles raw-mode, terminal resize, and signal forwarding.
func RunInteractive(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string) error {
	return RunInteractiveWithStartHook(ctx, config, hostConfig, name, nil)
}

// RunInteractiveWithStartHook runs an interactive container and invokes
// onStarted after Docker reports the named container is running.
func RunInteractiveWithStartHook(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string, onStarted func()) error {
	if ctx == nil {
		ctx = context.Background()
	}
	args := buildRunArgs(config, hostConfig, name, runMode{Interactive: true, TTY: true})
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return classifyRunError(args, err, "")
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	if onStarted != nil && strings.TrimSpace(name) != "" {
		if err, done := waitForContainerRunning(ctx, name, waitCh); done {
			return classifyRunError(args, err, "")
		}
		select {
		case err := <-waitCh:
			return classifyRunError(args, err, "")
		default:
		}
		onStarted()
	}
	return classifyRunError(args, <-waitCh, "")
}

func waitForContainerRunning(ctx context.Context, name string, waitCh <-chan error) (error, bool) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-waitCh:
			return err, true
		case <-ticker.C:
			info, err := ContainerInspect(ctx, name)
			if err == nil && info.State != nil && info.State.Running {
				return nil, false
			}
		}
	}
}

// RunDetached starts a container in the background and returns its ID.
func RunDetached(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string) (string, error) {
	args := buildRunArgs(config, hostConfig, name, runMode{Detach: true})
	id, err := capture(ctx, args...)
	if err != nil {
		return "", err
	}
	return id, nil
}

// RunDetachedInteractive starts a detached container with stdin open and a TTY,
// so it can later be reattached interactively.
func RunDetachedInteractive(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string) (string, error) {
	args := buildRunArgs(config, hostConfig, name, runMode{Detach: true, Interactive: true, TTY: true})
	id, err := capture(ctx, args...)
	if err != nil {
		return "", err
	}
	return id, nil
}
