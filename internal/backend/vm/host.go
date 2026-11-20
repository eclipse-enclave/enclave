// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	"enclave/internal/backend"
)

// host is every way the backend touches host processes and the network.
// Tests replace it, so the planning and orchestration run without KVM.
type host interface {
	LookPath(file string) (string, error)
	// Output runs a command to completion and returns its combined output.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	// Daemonize runs a command that leaves a daemon behind and returns the
	// command's output once it exits. The daemon inherits keep, and with it
	// whatever lock that file holds.
	Daemonize(ctx context.Context, name string, args []string, keep *os.File) ([]byte, error)
	// Start launches a helper that keeps running, also after this process,
	// with its output in logPath. It returns the helper's PID and a function
	// that stops it.
	Start(name string, args []string, logPath string) (pid int, stop func(), err error)
	// Interactive runs a command on the session's stdio and returns its exit
	// status.
	Interactive(ctx context.Context, name string, args []string, attach backend.AttachIO) (int, error)
	// Download writes the body of url to w.
	Download(ctx context.Context, url string, w io.Writer) error
}

type execHost struct{}

func (execHost) LookPath(file string) (string, error) { return exec.LookPath(file) }

func (execHost) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return runCaptured(ctx, name, args, nil)
}

func (execHost) Daemonize(ctx context.Context, name string, args []string, keep *os.File) ([]byte, error) {
	return runCaptured(ctx, name, args, keep)
}

// runCaptured collects the command's output in a file, not a pipe: a
// daemonizing QEMU hands its output descriptors to the daemon, and waiting
// for a pipe to close would then wait for the VM to exit.
func runCaptured(ctx context.Context, name string, args []string, inherit *os.File) ([]byte, error) {
	capture, err := os.CreateTemp("", "enclave-vm-output-")
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = capture.Close()
		_ = os.Remove(capture.Name())
	}()
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- argv is built by this package from validated request fields.
	cmd.Stdout = capture
	cmd.Stderr = capture
	if inherit != nil {
		cmd.ExtraFiles = []*os.File{inherit}
	}
	runErr := cmd.Run()
	out, err := os.ReadFile(capture.Name())
	if err != nil {
		return nil, err
	}
	return out, runErr
}

func (execHost) Start(name string, args []string, logPath string) (int, func(), error) {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- log path is inside the session runtime directory.
	if err != nil {
		return 0, nil, err
	}
	cmd := exec.Command(name, args...) // #nosec G204 -- argv is built by this package from validated request fields.
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	ownSession(cmd)
	err = cmd.Start()
	_ = logFile.Close()
	if err != nil {
		return 0, nil, err
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return cmd.Process.Pid, func() {
		select {
		case <-done:
			return
		default:
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}, nil
}

func (execHost) Interactive(ctx context.Context, name string, args []string, attach backend.AttachIO) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- argv is built by this package from validated request fields.
	cmd.Stdin = attach.In
	cmd.Stdout = attach.Out
	cmd.Stderr = attach.Err
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	cmd.Env = attach.CommandEnv()
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 0, err
	}
	return 0, nil
}

func (execHost) Download(ctx context.Context, url string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req) // #nosec G107 -- the URL is a pinned constant of this package.
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}
