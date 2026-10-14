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

func TestWrapSelectsPinnedClients(t *testing.T) {
	const pinnedClaude, pinnedGitHub = "2.1.63 (Claude Code)", "gh version 2.87.3 (fixture)"
	for _, tc := range []struct {
		name                     string
		claude, github           bool
		claudeVersion, ghVersion string
		wantError                bool
	}{
		{name: "no selection", claudeVersion: pinnedClaude, ghVersion: pinnedGitHub},
		{name: "Claude", claude: true, claudeVersion: pinnedClaude, ghVersion: pinnedGitHub},
		{name: "Claude and GitHub", claude: true, github: true, claudeVersion: pinnedClaude, ghVersion: pinnedGitHub},
		{name: "missing Claude", claude: true, ghVersion: pinnedGitHub, wantError: true},
		{name: "wrong Claude", claude: true, claudeVersion: "2.1.64 (Claude Code)", ghVersion: pinnedGitHub, wantError: true},
		{name: "missing GitHub", claude: true, github: true, claudeVersion: pinnedClaude, wantError: true},
		{name: "wrong GitHub", claude: true, github: true, claudeVersion: pinnedClaude, ghVersion: "gh version 2.87.4 (fixture)", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			ordinary := filepath.Join(home, "ordinary")
			writeClient(t, filepath.Join(ordinary, "claude"), "9.9.9 (Claude Code)")
			writeClient(t, filepath.Join(ordinary, "gh"), "gh version 9.9.9")
			claude := ClaudeLauncher(home)
			if tc.claudeVersion != "" {
				writeClient(t, claude, tc.claudeVersion)
			}
			ghPrefix := filepath.Join(home, "gh-pin")
			gh := filepath.Join(ghPrefix, "bin", "gh")
			if tc.ghVersion != "" {
				writeClient(t, gh, tc.ghVersion)
			}
			selected := filepath.Join(home, "selected")
			argv := Wrap(home, tc.claude, tc.github, []string{"sh", "-c", `{ command -v claude; command -v gh; } > "$0"`, selected})
			for i := range argv {
				argv[i] = strings.ReplaceAll(argv[i], "/opt/enclave/gh-2.87.3", ghPrefix)
			}
			cmd := exec.Command(argv[0], argv[1:]...) // #nosec G204 -- test runs the wrapper it builds.
			cmd.Env = []string{"HOME=" + home, "PATH=" + ordinary + string(os.PathListSeparator) + os.Getenv("PATH")}
			output, err := cmd.CombinedOutput()
			if tc.wantError {
				if err == nil {
					t.Fatal("command ran without its required pinned client")
				}
				if _, err := os.Stat(selected); !os.IsNotExist(err) {
					t.Fatal("command ran before its pinned clients were validated")
				}
				return
			}
			if err != nil {
				t.Fatalf("wrapped command: %v: %s", err, output)
			}
			wantClaude, wantGitHub := filepath.Join(ordinary, "claude"), filepath.Join(ordinary, "gh")
			if tc.claude {
				wantClaude = claude
			}
			if tc.github {
				wantGitHub = gh
			}
			data, err := os.ReadFile(selected)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := string(data), wantClaude+"\n"+wantGitHub+"\n"; got != want {
				t.Fatalf("selected clients:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
