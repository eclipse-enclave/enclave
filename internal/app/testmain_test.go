// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Prevent host XDG environment variables from overriding the home-based
	// path resolution used by tests. Without this, tests that pass t.TempDir()
	// as home share the real host XDG directories, causing cross-test pollution.
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		if err := os.Unsetenv(key); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}

func TestRunHelpAndCompletionWithoutWorkingDirectory(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "help", args: []string{"--help"}},
		{name: "completion script", args: []string{"completion", "bash"}},
		{name: "dynamic completion", args: []string{"__complete", ""}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			originalDir, err := os.Getwd()
			if err != nil {
				t.Fatalf("get original working directory: %v", err)
			}
			removedDir := t.TempDir()
			if err := os.Chdir(removedDir); err != nil {
				t.Fatalf("change working directory: %v", err)
			}
			t.Cleanup(func() {
				if err := os.Chdir(originalDir); err != nil {
					t.Errorf("restore working directory: %v", err)
				}
			})
			if err := os.Remove(removedDir); err != nil {
				t.Skipf("cannot remove current working directory: %v", err)
			}
			if _, err := os.Getwd(); err == nil {
				t.Skip("removing the current working directory did not make it unresolvable")
			}

			exitCode := -1
			discardStdout(t, func() {
				exitCode = Run(tc.args)
			})
			if exitCode != 0 {
				t.Fatalf("Run(%q) returned %d, want 0", tc.args, exitCode)
			}
		})
	}
}

func discardStdout(t *testing.T, fn func()) {
	t.Helper()

	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	original := os.Stdout
	os.Stdout = output
	defer func() {
		os.Stdout = original
		if err := output.Close(); err != nil {
			t.Errorf("close %s: %v", os.DevNull, err)
		}
	}()

	fn()
}
