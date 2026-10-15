// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebianRulesGoSelection(t *testing.T) {
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is unavailable")
	}
	rules, err := os.ReadFile(filepath.Join("..", "debian", "rules"))
	if err != nil {
		t.Fatal(err)
	}
	for _, versioned := range []bool{false, true} {
		name := "default"
		if versioned {
			name = "versioned"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			defaultBin := filepath.Join(root, "default")
			versionedBin := filepath.Join(root, "versioned")
			writeTestExecutable(t, filepath.Join(defaultBin, "go"), "#!/bin/sh\necho default\n")
			if versioned {
				writeTestExecutable(t, filepath.Join(versionedBin, "go"), "#!/bin/sh\necho versioned\n")
			}
			contents := strings.ReplaceAll(string(rules), "/usr/lib/go-1.24/bin", versionedBin)
			contents += "\n.PHONY: probe\nprobe:\n\t@go version\n"
			rulesPath := filepath.Join(root, "rules")
			if err := os.WriteFile(rulesPath, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(makePath, "--no-print-directory", "-f", rulesPath, "BUILD_VERSION=test", "probe")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+defaultBin+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("make probe: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != name {
				t.Fatalf("selected %q, want %q", got, name)
			}
		})
	}
}

func TestDebianRulesCleanReadOnlyToolchain(t *testing.T) {
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is unavailable")
	}
	rulesPath, err := filepath.Abs(filepath.Join("..", "debian", "rules"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	toolchain := filepath.Join(root, ".gomodcache", "golang.org", "toolchain", "bin")
	writeTestExecutable(t, filepath.Join(toolchain, "go"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(toolchain, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(toolchain, 0o755) })
	cmd := exec.Command(makePath, "--no-print-directory", "-f", rulesPath, "BUILD_VERSION=test", "override_dh_auto_clean")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make clean: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(root, ".gomodcache")); !os.IsNotExist(err) {
		t.Fatalf("module cache was not removed: %v", err)
	}
}

func writeTestExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}
