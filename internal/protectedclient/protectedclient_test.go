// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package protectedclient

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeClient(t *testing.T, file, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '" + version + "'; fi\n"
	if err := os.WriteFile(file, []byte(script), 0o700); err != nil { // #nosec G306 -- test client must be executable.
		t.Fatal(err)
	}
}

func TestWrapSelectsPinnedGitHubCLI(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		wantError     bool
	}{
		{name: "pinned", version: "gh version 2.87.3 (fixture)"},
		{name: "missing", wantError: true},
		{name: "wrong", version: "gh version 2.87.4 (fixture)", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			ordinary := filepath.Join(home, "ordinary")
			writeClient(t, filepath.Join(ordinary, "gh"), "gh version 9.9.9")
			prefix := filepath.Join(home, "gh-pin")
			gh := filepath.Join(prefix, "bin", "gh")
			if tc.version != "" {
				writeClient(t, gh, tc.version)
			}
			selected := filepath.Join(home, "selected")
			argv := Wrap([]string{"sh", "-c", `command -v gh > "$0"`, selected})
			for i := range argv {
				argv[i] = strings.ReplaceAll(argv[i], "/opt/enclave/gh-2.87.3", prefix)
			}
			cmd := exec.Command(argv[0], argv[1:]...) // #nosec G204 -- test runs the wrapper it builds.
			cmd.Env = []string{"HOME=" + home, "PATH=" + ordinary + string(os.PathListSeparator) + os.Getenv("PATH")}
			output, err := cmd.CombinedOutput()
			if tc.wantError {
				if err == nil {
					t.Fatal("command ran without the pinned GitHub CLI")
				}
				if _, err := os.Stat(selected); !os.IsNotExist(err) {
					t.Fatal("command ran before the pinned GitHub CLI was validated")
				}
				return
			}
			if err != nil {
				t.Fatalf("wrapped command: %v: %s", err, output)
			}
			data, err := os.ReadFile(selected)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != gh+"\n" {
				t.Fatalf("selected GitHub CLI %q, want %q", data, gh)
			}
		})
	}
}
