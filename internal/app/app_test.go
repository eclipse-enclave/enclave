// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunRejectsEmptyBackendBeforeDispatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))

	previousDetect := detectContainerCLIs
	detectContainerCLIs = func() []string {
		t.Fatal("an explicit backend must not trigger engine detection")
		return nil
	}
	t.Cleanup(func() { detectContainerCLIs = previousDetect })

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previousStderr := os.Stderr
	os.Stderr = writer
	defer func() { os.Stderr = previousStderr }()

	// cleanup --dry-run would otherwise succeed without selecting a backend.
	code := Run([]string{"cleanup", "--all", "--dry-run", "--backend="})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), `unsupported backend ""`) {
		t.Fatalf("Run returned %d with stderr %q", code, output)
	}
}

func TestRunVersionWithoutWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not allow removing the current working directory")
	}

	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get current directory: %v", err)
	}
	deletedDir := filepath.Join(t.TempDir(), "deleted")
	if err := os.Mkdir(deletedDir, 0o755); err != nil {
		t.Fatalf("create working directory: %v", err)
	}
	if err := os.Chdir(deletedDir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()
	if err := os.Remove(deletedDir); err != nil {
		t.Fatalf("remove working directory: %v", err)
	}

	out := captureStdout(t, func() {
		if code := Run([]string{"version"}); code != 0 {
			t.Fatalf("Run(version) returned %d", code)
		}
	})
	if !strings.HasPrefix(out, "enclave: ") || strings.Count(out, "\n") != 1 {
		t.Fatalf("unexpected version output %q", out)
	}

	flagOut := captureStdout(t, func() {
		if code := Run([]string{"--version"}); code != 0 {
			t.Fatalf("Run(--version) returned %d", code)
		}
	})
	if flagOut != out {
		t.Fatalf("--version output %q differs from version output %q", flagOut, out)
	}

	out = captureStdout(t, func() {
		if code := Run([]string{"version", "--json"}); code != 0 {
			t.Fatalf("Run(version --json) returned %d", code)
		}
	})
	var version struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Date    string `json:"date"`
	}
	if err := json.Unmarshal([]byte(out), &version); err != nil {
		t.Fatalf("decode version JSON %q: %v", out, err)
	}
	if version.Version == "" || version.Commit == "" || version.Date == "" {
		t.Fatalf("incomplete version JSON: %+v", version)
	}
}
