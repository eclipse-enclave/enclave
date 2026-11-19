// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package logx

import (
	"io"
	"os"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	return captureFile(t, &os.Stdout, fn)
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	return captureFile(t, &os.Stderr, fn)
}

// captureFile swaps *target for a pipe around fn and returns what fn wrote.
func captureFile(t *testing.T, target **os.File, fn func()) string {
	t.Helper()

	original := *target
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	*target = writer

	defer func() {
		*target = original
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	return string(data)
}

func TestInfofDefaultLineEnding(t *testing.T) {
	output := captureStdout(t, func() { Infof("hello %s", "world") })
	if !strings.HasSuffix(output, "hello world\n") || strings.Contains(output, "\r") {
		t.Fatalf("Infof output = %q, want plain newline ending without carriage returns", output)
	}
}

func TestInfofRawTTYLineEnding(t *testing.T) {
	original := isRawTTY
	isRawTTY = func(io.Writer) bool { return true }
	defer func() { isRawTTY = original }()

	output := captureStdout(t, func() { Infof("hello") })
	if !strings.HasPrefix(output, "\r") || !strings.HasSuffix(output, "hello\r\n") {
		t.Fatalf("Infof output = %q, want carriage-return prefix and CRLF ending on a raw terminal", output)
	}
}

func TestSuccessNoticefWritesToStderr(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() { SuccessNoticef("done %d", 1) })
	})
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if stderr != "ok: done 1\n" {
		t.Errorf("stderr = %q, want %q", stderr, "ok: done 1\n")
	}
}
