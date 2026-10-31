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
	"path/filepath"
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
	_, err := RunWithStartHook(ctx, config, hostConfig, name, nil)
	return err
}

// RunWithStartHook runs a container to completion, discarding its output, and
// invokes onStarted with the container's ID once it is running. It returns the
// ID of the container this run created, if any.
func RunWithStartHook(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string, onStarted func(id string)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cid, err := newCIDFile()
	if err != nil {
		return "", err
	}
	defer cid.remove()
	args := buildRunArgs(config, hostConfig, name, runMode{CIDFile: cid.path})
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204 -- args built from caller config, passed without a shell.
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	err = runCommandWithStartHook(ctx, cid, cmd, onStarted, nil)
	return cid.id(), classifyRunError(args, err, stderr.String())
}

// RunWithIOAndStartHook runs a container wired to the supplied streams and
// invokes onStarted with the container's ID once it is running. A non-empty
// detachKeys replaces the engine's detach keys of an attached terminal. It
// returns the ID of the container this run created, if any.
func RunWithIOAndStartHook(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string, in io.Reader, out io.Writer, errOut io.Writer, tty bool, detachKeys string, onStarted func(id string)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cid, err := newCIDFile()
	if err != nil {
		return "", err
	}
	defer cid.remove()
	mode := runMode{Interactive: in != nil, TTY: tty, DetachKeys: detachKeys, CIDFile: cid.path}
	args := buildRunArgs(config, hostConfig, name, mode)
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = errOut
	err = runCommandWithStartHook(ctx, cid, cmd, onStarted, nil)
	return cid.id(), classifyRunError(args, err, "")
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
	_, err := RunInteractiveWithStartHook(ctx, config, hostConfig, name, "", nil, nil)
	return err
}

// RunInteractiveWithStartHook runs an interactive container and invokes
// onStarted with the container's ID once it is running. A non-empty detachKeys
// replaces the engine's detach keys. SIGINT, SIGTERM and SIGHUP sent to this
// process while the child runs are forwarded to it. A non-nil env replaces the
// client's inherited environment. It returns the ID of the container this run
// created, if any.
func RunInteractiveWithStartHook(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string, detachKeys string, env []string, onStarted func(id string)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cid, err := newCIDFile()
	if err != nil {
		return "", err
	}
	defer cid.remove()
	args := buildRunArgs(config, hostConfig, name, runMode{Interactive: true, TTY: true, DetachKeys: detachKeys, CIDFile: cid.path})
	cmd := exec.CommandContext(ctx, dockerBinary, args...) // #nosec G204
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	child := make(chan *os.Process, 1)
	stopRelay := relayInterrupts(child)
	defer stopRelay()
	err = runCommandWithStartHook(ctx, cid, cmd, onStarted, child)
	return cid.id(), classifyRunError(args, err, "")
}

// cidFile is the --cidfile of one run: it names the container that run
// created, never another one holding the same name.
type cidFile struct {
	dir  string
	path string
}

// The engine refuses an existing --cidfile, so the path is fresh but unused.
func newCIDFile() (cidFile, error) {
	dir, err := os.MkdirTemp("", "enclave-cid-")
	if err != nil {
		return cidFile{}, err
	}
	return cidFile{dir: dir, path: filepath.Join(dir, "cid")}, nil
}

// id returns the created container's ID, or "" before creation. Docker may
// hold an empty file before then; podman deletes it when --rm removes the
// container.
func (c cidFile) id() string {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return ""
	}
	return containerIDFromRunOutput(string(data))
}

func (c cidFile) remove() {
	_ = os.RemoveAll(c.dir)
}

// runCommandWithStartHook starts cmd and invokes onStarted once the container
// it created is running. A non-nil child receives the started process, which
// is how the interactive path gets signals relayed to the engine.
func runCommandWithStartHook(ctx context.Context, cid cidFile, cmd *exec.Cmd, onStarted func(id string), child chan<- *os.Process) error {
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
	if onStarted != nil {
		id, err, done := waitForContainerRunning(ctx, cid, waitCh)
		if done {
			return err
		}
		select {
		case err := <-waitCh:
			return err
		default:
		}
		onStarted(id)
	}
	return <-waitCh
}

// relayInterrupts forwards SIGINT, SIGTERM and SIGHUP aimed at this process to
// the engine child once it is known. Terminal Ctrl-C needs no help: the engine
// keeps the TTY in raw mode and proxies it into the container. A signal sent to
// this process directly, by a supervisor or kill, would otherwise be absorbed
// by the caller's interrupt handling and leave the session running. The
// signals are registered before the child starts so none is lost in between;
// one that arrives early is delivered as soon as the child exists. The
// returned function stops relaying. When the terminal closes, the kernel
// already sends SIGHUP to the child, which shares our process group, so the
// relayed copy is a duplicate; the relay matters for kill -HUP on enclave.
func relayInterrupts(child <-chan *os.Process) func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
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

// waitForContainerRunning returns the created container's ID once it runs, or
// done with the run's result when the run ends first.
func waitForContainerRunning(ctx context.Context, cid cidFile, waitCh <-chan error) (id string, err error, done bool) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-waitCh:
			return "", err, true
		case <-ticker.C:
			id := cid.id()
			if id == "" {
				continue
			}
			info, err := ContainerInspect(ctx, id)
			if err == nil && info.State != nil && info.State.Running {
				return info.ID, nil, false
			}
		}
	}
}

// RunDetached starts a container in the background and returns its ID. When
// the run fails after the container was created, the ID the engine printed is
// returned alongside the error so the caller can remove exactly that
// container: Docker prints it before attempting the start, podman prints
// nothing on failure.
func RunDetached(ctx context.Context, config *ContainerConfig, hostConfig *HostConfig, name string) (string, error) {
	args := buildRunArgs(config, hostConfig, name, runMode{Detach: true})
	out, err := capture(ctx, args...)
	if err != nil {
		return containerIDFromRunOutput(out), err
	}
	return out, nil
}

const fullContainerIDLength = 64

// Partial or unexpected output must never become a removal target.
func containerIDFromRunOutput(out string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	line = strings.TrimSpace(line)
	if len(line) != fullContainerIDLength || strings.TrimLeft(line, "0123456789abcdef") != "" {
		return ""
	}
	return line
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
