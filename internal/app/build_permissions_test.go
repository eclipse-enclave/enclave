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
	"strings"
	"testing"
)

func TestCollectAppRootModeIssuesReportsRestrictiveAssets(t *testing.T) {
	root := t.TempDir()
	writeModeTestFile(t, filepath.Join(root, "entrypoint.sh"), 0o644)
	writeModeTestFile(t, filepath.Join(root, "runtime-assets", "build-scripts", "lib", "common.sh"), 0o640)
	writeModeTestFile(t, filepath.Join(root, "runtime-assets", "build-scripts", "bin", "helper"), 0o644)
	writeModeTestFile(t, filepath.Join(root, "runtime-assets", "auth-reconcile.sh"), 0o640)
	writeModeTestFile(t, filepath.Join(root, "runtime-assets", "net.sh"), 0o644)
	writeModeTestFile(t, filepath.Join(root, "runtime-assets", "tmux-session.conf"), 0o644)
	writeModeTestFile(t, filepath.Join(root, "extensions", "tools", "claude", "spec.yaml"), 0o640)
	writeModeTestFile(t, filepath.Join(root, "extensions", "tools", "claude", "install.sh"), 0o644)

	issues, err := collectAppRootModeIssues(root)
	if err != nil {
		t.Fatalf("collectAppRootModeIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	for _, want := range []string{
		"entrypoint.sh lacks world execute permission",
		"runtime-assets/build-scripts/lib/common.sh lacks world read/execute permission",
		"runtime-assets/build-scripts/bin/helper lacks world execute permission",
		"runtime-assets/auth-reconcile.sh lacks world read permission",
		"extensions/tools/claude/spec.yaml lacks world read permission",
		"extensions/tools/claude/install.sh lacks world execute permission",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected issue containing %q, got:\n%s", want, joined)
		}
	}
}

func writeModeTestFile(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("x\n"), 0o666); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}
