// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package sbx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"enclave/internal/backend"
	"enclave/internal/logx"
)

const defaultBinary = "sbx"

// runner drives the sbx CLI. Tests replace it with a recorder.
type runner interface {
	// Output runs sbx without a terminal and returns its stdout.
	Output(ctx context.Context, args ...string) ([]byte, error)
	// Run runs sbx without a terminal, sending its output to progress.
	Run(ctx context.Context, progress io.Writer, args ...string) error
	// Attach runs sbx on the session's terminal and returns its exit status.
	// It is not bound to a context: once attached, Ctrl-C belongs to the tool.
	Attach(args []string, attach backend.AttachIO) (int, error)
}

type cliRunner struct {
	binary string
}

func (c cliRunner) Output(ctx context.Context, args ...string) ([]byte, error) {
	logx.Debugf("sbx backend: %s %s", c.binary, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, c.binary, args...) // #nosec G204 -- args are generated from validated request fields.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, commandError(args, stderr.String(), err)
	}
	return out, nil
}

func (c cliRunner) Run(ctx context.Context, progress io.Writer, args ...string) error {
	logx.Debugf("sbx backend: %s %s", c.binary, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, c.binary, args...) // #nosec G204 -- args are generated from validated request fields.
	var stderr bytes.Buffer
	cmd.Stdout = progress
	cmd.Stderr = io.MultiWriter(progress, &stderr)
	if err := cmd.Run(); err != nil {
		return commandError(args, stderr.String(), err)
	}
	return nil
}

func (c cliRunner) Attach(args []string, attach backend.AttachIO) (int, error) {
	logx.Debugf("sbx backend: %s %s", c.binary, strings.Join(args, " "))
	cmd := exec.Command(c.binary, args...) // #nosec G204 -- args are generated from validated request fields.
	cmd.Stdin = orStdin(attach.In)
	cmd.Stdout = orWriter(attach.Out, os.Stdout)
	cmd.Stderr = orWriter(attach.Err, os.Stderr)
	cmd.Env = attach.CommandEnv()
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 1, fmt.Errorf("sbx backend: run %s: %w", c.binary, err)
	}
	return 0, nil
}

func commandError(args []string, stderr string, err error) error {
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	if text := strings.TrimSpace(stderr); text != "" {
		return fmt.Errorf("sbx %s: %s", verb, text)
	}
	return fmt.Errorf("sbx %s: %w", verb, err)
}

func orStdin(r io.Reader) io.Reader {
	if r != nil {
		return r
	}
	return os.Stdin
}

func orWriter(w io.Writer, fallback io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return fallback
}
