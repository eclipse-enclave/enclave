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
	"os/signal"
	"strings"
	"syscall"
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
	return RunWithStartHook(ctx, config, hostConfig, name, nil)
}

// RunWithStartHook runs a container to completion, discarding its output, and
// invokes onStarted after Docker reports the named container is running.
func RunWithStartHook(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string, onStarted func()) error {
	if ctx == nil {
		ctx = context.Background()
	}
	args := buildRunArgs(config, hostConfig, name, runMode{})
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204 -- args built from caller config, passed without a shell.
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	return classifyRunError(args, runCommandWithStartHook(ctx, name, cmd, onStarted, nil), stderr.String())
}

// RunWithIOAndStartHook runs a container wired to the supplied streams and
// invokes onStarted after Docker reports the named container is running.
func RunWithIOAndStartHook(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string, in io.Reader, out io.Writer, errOut io.Writer, tty bool, onStarted func()) error {
	if ctx == nil {
		ctx = context.Background()
	}
	mode := runMode{Interactive: in != nil, TTY: tty}
	args := buildRunArgs(config, hostConfig, name, mode)
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = errOut
	return classifyRunError(args, runCommandWithStartHook(ctx, name, cmd, onStarted, nil), "")
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
// onStarted after Docker reports the named container is running. SIGINT and
// SIGTERM sent to this process while the child runs are forwarded to it.
func RunInteractiveWithStartHook(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string, onStarted func()) error {
	if ctx == nil {
		ctx = context.Background()
	}
	args := buildRunArgs(config, hostConfig, name, runMode{Interactive: true, TTY: true})
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	child := make(chan *os.Process, 1)
	stopRelay := relayInterrupts(child)
	defer stopRelay()
	return classifyRunError(args, runCommandWithStartHook(ctx, name, cmd, onStarted, child), "")
}

// runCommandWithStartHook starts cmd and invokes onStarted once the named
// container is running. A non-nil child receives the started process, which is
// how the interactive path gets signals relayed to the engine.
func runCommandWithStartHook(ctx context.Context, name string, cmd *exec.Cmd, onStarted func(), child chan<- *os.Process) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	if child != nil {
		child <- cmd.Process
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	if onStarted != nil && strings.TrimSpace(name) != "" {
		if err, done := waitForContainerRunning(ctx, name, waitCh); done {
			return err
		}
		select {
		case err := <-waitCh:
			return err
		default:
		}
		onStarted()
	}
	return <-waitCh
}

// relayInterrupts forwards SIGINT and SIGTERM aimed at this process to the
// engine child once it is known. Terminal Ctrl-C needs no help: the engine
// keeps the TTY in raw mode and proxies it into the container. A signal sent to
// this process directly, by a supervisor or kill, would otherwise be absorbed
// by the caller's interrupt handling and leave the session running. The
// signals are registered before the child starts so none is lost in between;
// one that arrives early is delivered as soon as the child exists. The
// returned function stops relaying.
func relayInterrupts(child <-chan *os.Process) func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		var proc *os.Process
		select {
		case proc = <-child:
		case <-done:
			return
		}
		for {
			select {
			case sig := <-signals:
				_ = proc.Signal(sig)
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
	}
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

// ContainerCreate creates a container without starting it and returns its ID.
func ContainerCreate(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string) (string, error) {
	args := buildRunArgs(config, hostConfig, name, runMode{Create: true})
	return capture(ctx, args...)
}
