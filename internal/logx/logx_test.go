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

	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdout = writer

	defer func() {
		os.Stdout = original
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
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
