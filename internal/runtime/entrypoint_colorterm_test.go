// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEntrypoint_COLORTERMDefaultsToTruecolorWhenUnset(t *testing.T) {
	value, output, err := runEntrypointCaptureCOLORTERM(t, nil)
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}
	if value != "truecolor" {
		t.Fatalf("expected COLORTERM=truecolor, got %q", value)
	}
}

func TestEntrypoint_COLORTERMPreservesExplicitValue(t *testing.T) {
	value, output, err := runEntrypointCaptureCOLORTERM(t, []string{"COLORTERM=24bit"})
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}
	if value != "24bit" {
		t.Fatalf("expected COLORTERM=24bit, got %q", value)
	}
}

func runEntrypointCaptureCOLORTERM(t *testing.T, extraEnv []string) (string, string, error) {
	t.Helper()

	home, out, err := runEntrypointCommand(t, extraEnv, "bash", "-lc", `printf "%s" "${COLORTERM:-}" > "$HOME/colorterm.out"`)
	valueBytes, readErr := os.ReadFile(filepath.Join(home, "colorterm.out"))
	if readErr != nil {
		t.Fatalf("read colorterm output: %v\nentrypoint output:\n%s", readErr, string(out))
	}

	return string(valueBytes), string(out), err
}
