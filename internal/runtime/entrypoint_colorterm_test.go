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

	home := t.TempDir()
	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	entrypointPath := filepath.Join("..", "..", "entrypoint.sh")
	cmd := exec.Command("bash", entrypointPath, "bash", "-lc", `printf "%s" "${COLORTERM:-}" > "$HOME/colorterm.out"`)
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
		"TOOL=pi",
	}
	if len(extraEnv) > 0 {
		env = append(env, extraEnv...)
	}
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	valueBytes, readErr := os.ReadFile(filepath.Join(home, "colorterm.out"))
	if readErr != nil {
		t.Fatalf("read colorterm output: %v\nentrypoint output:\n%s", readErr, string(out))
	}

	return string(valueBytes), string(out), err
}
