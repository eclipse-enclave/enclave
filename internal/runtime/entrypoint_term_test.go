// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEntrypoint_TERMFallsBackWhenTerminfoMissing(t *testing.T) {
	requireInfocmp(t)
	value, output, err := runEntrypointCaptureTERM(t, []string{"TERM=enclave-no-such-term"})
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}
	if value != "xterm-256color" {
		t.Fatalf("expected TERM=xterm-256color, got %q", value)
	}
}

func TestEntrypoint_TERMPreservesKnownValue(t *testing.T) {
	requireInfocmp(t)
	value, output, err := runEntrypointCaptureTERM(t, []string{"TERM=xterm"})
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}
	if value != "xterm" {
		t.Fatalf("expected TERM=xterm, got %q", value)
	}
}

func requireInfocmp(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("infocmp"); err != nil {
		t.Skip("infocmp not available")
	}
}

func runEntrypointCaptureTERM(t *testing.T, extraEnv []string) (string, string, error) {
	t.Helper()

	home, out, err := runEntrypointCommand(t, extraEnv, "bash", "-c", `printf "%s" "${TERM:-}" > "$HOME/term.out"`)
	valueBytes, readErr := os.ReadFile(filepath.Join(home, "term.out"))
	if readErr != nil {
		t.Fatalf("read term output: %v\nentrypoint output:\n%s", readErr, out)
	}

	return string(valueBytes), out, err
}
