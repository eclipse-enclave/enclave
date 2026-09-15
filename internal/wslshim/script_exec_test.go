// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package wslshim

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The launcher sends these two scripts to a shell inside the distribution on
// every run, but it only ever runs them on Windows. A POSIX shell is a POSIX
// shell, so the host's serves to check that they parse, behave, and — for the
// probe — produce output the Go side reads back.

func shellPath(t *testing.T) string {
	t.Helper()

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX shell available: %v", err)
	}
	return sh
}

// installFakeEnclave creates an executable at dir/.local/bin/enclave, which is
// the location `make install` uses and the probe's first fallback.
func installFakeEnclave(t *testing.T, dir string) string {
	t.Helper()

	binDir := filepath.Join(dir, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatalf("create %s: %v", binDir, err)
	}
	path := filepath.Join(binDir, "enclave")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestProbeScriptRoundTrip is the check the marker exists for: the script and
// parseProbeOutput have to agree, and a login shell that writes to standard
// output must not be able to displace the answer.
func TestProbeScriptRoundTrip(t *testing.T) {
	home := t.TempDir()
	want := installFakeEnclave(t, home)

	cases := []struct {
		name    string
		chatter string
	}{
		{"quiet profile", ""},
		{"banner", "Welcome to Ubuntu 24.04 LTS\n"},
		{"banner without a trailing newline", "direnv: loading"},
		{"profile that echoes the marker", probeMarker + "not-the-answer\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// printf emits the chatter the way a profile would, then the probe
			// runs in the same shell and appends its answer.
			script := `printf %s "$ENCLAVE_TEST_CHATTER"` + "\n" + probeScript
			// #nosec G204
			// The script is this package's own constant plus a fixed literal.
			cmd := exec.Command(shellPath(t), "-c", script)
			cmd.Env = []string{"HOME=" + home, "PATH=/nonexistent", "ENCLAVE_TEST_CHATTER=" + tc.chatter}

			stdout, err := cmd.Output()
			if err != nil {
				t.Fatalf("probe script: %v", err)
			}

			got, ok := parseProbeOutput(stdout)
			if !ok {
				t.Fatalf("parseProbeOutput(%q) rejected the real script's output", stdout)
			}
			if got != want {
				t.Errorf("parseProbeOutput(%q) = %q, want %q", stdout, got, want)
			}
		})
	}
}

func TestProbeScriptPrefersPATH(t *testing.T) {
	home := t.TempDir()
	onPath := installFakeEnclave(t, t.TempDir())

	// #nosec G204
	cmd := exec.Command(shellPath(t), "-c", probeScript)
	cmd.Env = []string{"HOME=" + home, "PATH=" + filepath.Dir(onPath)}

	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe script: %v", err)
	}
	got, ok := parseProbeOutput(stdout)
	if !ok || got != onPath {
		t.Errorf("parseProbeOutput = %q, %v; want %q", got, ok, onPath)
	}
}

// The exit code is the launcher's signal that the answer is conclusive and the
// probe must not be retried.
func TestProbeScriptExitsNotFoundWhenNothingIsInstalled(t *testing.T) {
	// #nosec G204
	cmd := exec.Command(shellPath(t), "-c", probeScript)
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/nonexistent"}

	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("probe script err = %v, want an exit error", err)
	}
	if exitErr.ExitCode() != probeNotFoundCode {
		t.Errorf("exit code = %d, want %d", exitErr.ExitCode(), probeNotFoundCode)
	}
}

// TestCDScriptForwardsArgumentsVerbatim covers the fallback for a wsl.exe
// without --cd. It is the one path where a shell still stands between the
// launcher and enclave, so "$@" has to survive every argument shape.
func TestCDScriptForwardsArgumentsVerbatim(t *testing.T) {
	dir := t.TempDir()
	recorder := filepath.Join(dir, "record")
	out := filepath.Join(dir, "argv")
	// #nosec G306
	// A recorder in the test's own temp directory.
	script := "#!/bin/sh\npwd > " + out + ".pwd\nfor a in \"$@\"; do printf '%s\\0' \"$a\"; done > " + out + "\n"
	if err := os.WriteFile(recorder, []byte(script), 0o700); err != nil {
		t.Fatalf("write recorder: %v", err)
	}

	workdir := filepath.Join(dir, "my proj")
	if err := os.Mkdir(workdir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	want := []string{"--tool", "claude", "", "--", "-p", `say "hi" to C:\tmp\`, "a b"}
	args := append([]string{"-c", cdScript, "enclave-wsl-launcher", workdir, recorder}, want...)
	// #nosec G204
	cmd := exec.Command(shellPath(t), args...)
	if err := cmd.Run(); err != nil {
		t.Fatalf("cd script: %v", err)
	}

	raw, err := os.ReadFile(out) // #nosec G304
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}

	pwd, err := os.ReadFile(out + ".pwd") // #nosec G304
	if err != nil {
		t.Fatalf("read pwd: %v", err)
	}
	if strings.TrimSpace(string(pwd)) != workdir {
		t.Errorf("working directory = %q, want %q", strings.TrimSpace(string(pwd)), workdir)
	}
}

// An unreachable directory has to fail before enclave is execed, with the
// launcher-failure code rather than a code enclave could have produced.
func TestCDScriptFailsWhenTheDirectoryIsUnreachable(t *testing.T) {
	args := []string{"-c", cdScript, "enclave-wsl-launcher", filepath.Join(t.TempDir(), "absent"), "/bin/true"}
	// #nosec G204
	err := exec.Command(shellPath(t), args...).Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("cd script err = %v, want an exit error", err)
	}
	if exitErr.ExitCode() != exitLauncherFailure {
		t.Errorf("exit code = %d, want %d", exitErr.ExitCode(), exitLauncherFailure)
	}
}
