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
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
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

// ExecWithInput runs cmd in a running container with input on stdin and
// returns its trimmed stdout. On failure it also returns the stdout printed
// so far, so callers can decode a structured error response.
func ExecWithInput(ctx context.Context, container string, cmd []string, input []byte) (string, error) {
	if container == "" || len(cmd) == 0 {
		return "", fmt.Errorf("exec requires a container and a command")
	}
	args := append([]string{"exec", "--interactive", container}, cmd...)
	if input == nil {
		input = []byte{}
	}
	out, err := captureInput(ctx, input, args...)
	return strings.TrimSpace(out), err
}

// ExecStream runs cmd in a running container as user with stdin attached and
// returns its stdout as it is produced. Close ends the command. When the
// command fails, the read at the end of stdout returns a *cliError with its
// stderr instead of io.EOF.
func ExecStream(ctx context.Context, container, user string, cmd []string, stdin io.Reader) (io.ReadCloser, error) {
	if container == "" || len(cmd) == 0 {
		return nil, fmt.Errorf("exec requires a container and a command")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	args := []string{"exec", "--interactive"}
	if user != "" {
		args = append(args, "--user", user)
	}
	args = append(append(args, container), cmd...)
	execCtx, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(execCtx, dockerBinary, args...) // #nosec G204 -- args are fixed flags plus caller-provided command, passed without a shell.
	if stdin == nil {
		stdin = bytes.NewReader(nil)
	}
	command.Stdin = stdin
	// A stdin reader that never ends must not keep Close waiting.
	command.WaitDelay = execStreamWaitDelay
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stream := &execStream{stdout: stdout, cmd: command, cancel: cancel, args: args}
	command.Stderr = &stream.stderr
	if err := command.Start(); err != nil {
		cancel()
		return nil, &cliError{args: args, err: err}
	}
	return stream, nil
}

const execStreamWaitDelay = 5 * time.Second

type execStream struct {
	stdout io.ReadCloser
	cmd    *exec.Cmd
	cancel context.CancelFunc
	args   []string
	stderr bytes.Buffer
	once   sync.Once
	err    error
}

func (s *execStream) Read(p []byte) (int, error) {
	n, err := s.stdout.Read(p)
	if errors.Is(err, io.EOF) {
		if werr := s.wait(); werr != nil {
			return n, werr
		}
	}
	return n, err
}

func (s *execStream) wait() error {
	s.once.Do(func() {
		if err := s.cmd.Wait(); err != nil {
			code, _ := commandExitCode(err)
			s.err = &cliError{args: s.args, code: code, stderr: strings.TrimSpace(s.stderr.String()), err: err}
		}
	})
	return s.err
}

func (s *execStream) Close() error {
	s.cancel()
	_ = s.wait()
	return nil
}
