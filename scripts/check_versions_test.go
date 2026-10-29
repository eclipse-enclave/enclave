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

func TestCheckVersionsDevtoolsLockstep(t *testing.T) {
	for _, tool := range []string{"", "golangci-lint", "gosec", "govulncheck"} {
		name := tool
		if name == "" {
			name = "matching"
		}
		t.Run(name, func(t *testing.T) {
			root, _ := versionCheckFixture(t)
			if tool != "" {
				path := filepath.Join(root, "extensions", "features", "devtools", "README.md")
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(string(contents), "\n")
				for i, line := range lines {
					if strings.HasPrefix(line, "- `"+tool+"`") {
						lines[i] = "- `" + tool + "` (`v0.0.0`)"
					}
				}
				if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", "scripts/check-versions.sh", "--offline")
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if tool == "" {
				if err != nil {
					t.Fatalf("matching pins failed: %v\n%s", err, output)
				}
			} else if err == nil || !strings.Contains(string(output), tool+" version disagrees") {
				t.Fatalf("expected %s mismatch, got %v\n%s", tool, err, output)
			}
		})
	}
}

func TestCheckVersionsGoPatchCeiling(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is unavailable")
	}
	for _, tc := range []struct {
		floor string
		want  string
	}{
		{"1.24.0", "Go floor 1.24.0 may be raised to 1.24.4"},
		{"1.24.4", "Go floor 1.24.4 is at the ceiling"},
		{"1.24.9", "PROBLEM  Go floor 1.24.9 is above the ceiling 1.24.4"},
	} {
		t.Run(tc.floor, func(t *testing.T) {
			root, gitPath := versionCheckFixture(t)
			path := filepath.Join(root, "go.mod")
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			contents = []byte(strings.Replace(string(contents), "\ngo 1.24.0\n", "\ngo "+tc.floor+"\n", 1))
			if err := os.WriteFile(path, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "test-bin")
			writeTestExecutable(t, filepath.Join(bin, "git"), `#!/bin/sh
if [ "$1" = ls-remote ]; then exit 1; fi
exec "$CHECK_VERSIONS_REAL_GIT" "$@"
`)
			writeTestExecutable(t, filepath.Join(bin, "go"), "#!/bin/sh\nprintf '{}\\n'\n")
			writeTestExecutable(t, filepath.Join(bin, "curl"), `#!/bin/sh
for url in "$@"; do :; done
case "$url" in
    https://proxy.golang.org/golang.org/toolchain/@v/list)
        printf '%s\n' v0.0.1-go1.26.9.linux-amd64 v0.0.1-go1.27.1.linux-amd64
        ;;
    https://qa.debian.org/madison.php*|https://ubuntu-archive-team.ubuntu.com/madison.cgi*)
        for suite in trixie jammy noble resolute; do
            printf 'package | 2:1.24.4-1 | %s\n' "$suite"
        done
        ;;
    *) exit 1 ;;
esac
`)
			cmd := exec.Command("bash", "scripts/check-versions.sh")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "CHECK_VERSIONS_REAL_GIT="+gitPath)
			output, _ := cmd.CombinedOutput()
			// Other online lookups deliberately fail; only the mocked ceiling is under test.
			if !strings.Contains(string(output), tc.want) {
				t.Fatalf("missing %q in output:\n%s", tc.want, output)
			}
		})
	}
}

func versionCheckFixture(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is unavailable")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	for _, path := range []string{
		"scripts/check-versions.sh", "go.mod", "debian/control", "debian/rules",
		"packaging/rpm/enclave.spec", "README.md", "docs/DEV.md", "Makefile",
		"Dockerfile", "Dockerfile.gateway", "internal/model/types.go",
		"runtime-assets/microvm/alpine/build-bundle.sh",
		"extensions/features/devtools/install.sh", "extensions/features/devtools/README.md",
		".github/workflows/ci.yml", ".github/workflows/reusable-rpm-build.yml",
	} {
		contents, err := os.ReadFile(filepath.Join("..", path))
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
		cmd := exec.Command(gitPath, args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	return root, gitPath
}
