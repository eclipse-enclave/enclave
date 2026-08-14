// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
	if !strings.HasPrefix(out, "Enclave: ") || strings.Count(out, "\n") != 1 {
		t.Fatalf("unexpected version output %q", out)
	}
}
