// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

const (
	helperEnv  = "ENCLAVE_WSLSHIM_TEST_EXIT"
	fakeWSL    = `C:\Windows\System32\wsl.exe`
	fakeCWD    = `\\wsl.localhost\Ubuntu\home\p\proj`
	fakeBinary = "/home/p/.local/bin/enclave"
	// fakeBinaryOut is the probe's answer for fakeBinary. TestProbeScriptRoundTrip
	// runs the real script and checks it produces something parseProbeOutput
	// reads the same way, so this fixture cannot drift from it unnoticed.
	fakeBinaryOut = "\n" + probeMarker + fakeBinary + "\n"
)

// TestExitCodeHelperProcess is re-executed as a child so the tests can obtain a
// real *exec.ExitError on every platform, without depending on a shell.
func TestExitCodeHelperProcess(t *testing.T) {
	value := os.Getenv(helperEnv)
	if value == "" {
		t.Skip("not running as the exit-code helper")
	}
	code, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("invalid %s: %v", helperEnv, err)
	}
	os.Exit(code)
}

func exitError(t *testing.T, code int) error {
	t.Helper()

	// #nosec G204
	// The command is this test binary, re-executed with a fixed test filter.
	cmd := exec.Command(os.Args[0], "-test.run=^TestExitCodeHelperProcess$")
	cmd.Env = append(os.Environ(), helperEnv+"="+strconv.Itoa(code))
	err := cmd.Run()
	if code == 0 {
		return err
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("helper process did not produce an ExitError: %v", err)
	}
	return err
}

type response struct {
	stdout []byte
	stderr []byte
	err    error
}

// host records what the launcher asked of the system and replays scripted
// answers, so the orchestration can be exercised without WSL2.
type host struct {
	captures   []response
	spawnErr   error
	commands   []string
	spawned    string
	spawnEnv   []string
	stderr     bytes.Buffer
	wslMissing bool
}

func (h *host) capture(_, cmdLine string, _ []string) ([]byte, []byte, error) {
	h.commands = append(h.commands, cmdLine)
	if len(h.captures) == 0 {
		return nil, nil, fmt.Errorf("unexpected capture: %s", cmdLine)
	}
	next := h.captures[0]
	h.captures = h.captures[1:]
	return next.stdout, next.stderr, next.err
}

func (h *host) spawn(_, cmdLine string, env []string) error {
	h.commands = append(h.commands, cmdLine)
	h.spawned = cmdLine
	h.spawnEnv = env
	return h.spawnErr
}

func (h *host) shim(cwd string, environ []string) shim {
	return shim{
		getwd:        func() (string, error) { return cwd, nil },
		lookPath:     h.lookPath,
		environ:      environ,
		driveType:    fixedDriveTypes,
		resolveDrive: fixedDriveTargets,
		spawn:        h.spawn,
		capture:      h.capture,
		stderr:       &h.stderr,
	}
}

func (h *host) lookPath(string) (string, error) {
	if h.wslMissing {
		return "", errors.New("executable file not found in %PATH%")
	}
	return fakeWSL, nil
}

// probeOK is the answer of a distribution that has enclave installed.
func probeOK() response {
	return response{stdout: []byte(fakeBinaryOut)}
}

func TestExecuteForwardsArgumentsToTheResolvedBinary(t *testing.T) {
	h := &host{captures: []response{probeOK()}}

	code := h.shim(fakeCWD, nil).execute([]string{"--tool", "codex", "--", "-p", `fix the "auth" bug`})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, h.stderr.String())
	}

	want := []string{
		"-d", "Ubuntu", "--cd", "/home/p/proj", "-e", fakeBinary,
		"--tool", "codex", "--", "-p", `fix the "auth" bug`,
	}
	assertCommandLine(t, h.spawned, want)
}

// Every nonzero code has to come back unchanged. Zero is covered by the other
// tests, which all assert a successful run returns it; scripting it here would
// pass even with propagation removed, since exitError reports no error for 0.
func TestExecutePropagatesChildExitCode(t *testing.T) {
	for _, code := range []int{1, 2, 42, 125, 130, 255} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			h := &host{captures: []response{probeOK()}, spawnErr: exitError(t, code)}

			if got := h.shim(fakeCWD, nil).execute(nil); got != code {
				t.Errorf("exit code = %d, want %d; stderr: %s", got, code, h.stderr.String())
			}
		})
	}
}

func TestExecuteReportsWhenNoDistributionsAreInstalled(t *testing.T) {
	unusable := []byte("There is no distribution with the supplied name.")
	h := &host{captures: []response{
		{stderr: unusable, err: exitError(t, 1)},
		{stderr: unusable, err: exitError(t, 1)},
		{stdout: nil},
	}}

	if got := h.shim(fakeCWD, nil).execute(nil); got != exitLauncherFailure {
		t.Errorf("exit code = %d, want %d", got, exitLauncherFailure)
	}
	if !strings.Contains(h.stderr.String(), "No WSL distributions are installed") {
		t.Errorf("stderr does not report an empty distribution list: %q", h.stderr.String())
	}
}

func TestExecuteReportsLauncherFailureWhenWSLCannotStart(t *testing.T) {
	h := &host{captures: []response{probeOK()}, spawnErr: errors.New("access is denied")}

	if got := h.shim(fakeCWD, nil).execute(nil); got != exitLauncherFailure {
		t.Errorf("exit code = %d, want %d", got, exitLauncherFailure)
	}
	assertStderrHasPrefix(t, h.stderr.String())
}

func TestExecuteRefusesWhenWSLIsNotInstalled(t *testing.T) {
	h := &host{wslMissing: true}

	if got := h.shim(fakeCWD, nil).execute(nil); got != exitLauncherFailure {
		t.Errorf("exit code = %d, want %d", got, exitLauncherFailure)
	}
	if !strings.Contains(h.stderr.String(), "wsl --install") {
		t.Errorf("stderr does not say how to install WSL2: %q", h.stderr.String())
	}
}

// What a cmd.exe user gets from `pushd \\wsl.localhost\Ubuntu\home\p\proj`: the
// drive letter is an alias for a path the launcher accepts, so the session runs.
func TestExecuteRunsThroughADriveLetterMappingAWSLShare(t *testing.T) {
	h := &host{captures: []response{probeOK()}}

	code := h.shim(`Z:\sub`, nil).execute([]string{"continue"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, h.stderr.String())
	}

	assertCommandLine(t, h.spawned, []string{
		"-d", "Ubuntu", "--cd", "/home/p/proj/sub", "-e", fakeBinary, "continue",
	})
}

func TestExecuteRefusesADriveLetterMappingAnOrdinaryShare(t *testing.T) {
	h := &host{}

	if got := h.shim(`W:\proj`, nil).execute(nil); got != exitLauncherFailure {
		t.Errorf("exit code = %d, want %d", got, exitLauncherFailure)
	}
	if h.spawned != "" {
		t.Error("nothing should have been spawned")
	}
	assertStderrHasPrefix(t, h.stderr.String())
}

func TestExecuteRefusesWindowsDriveWithoutOptIn(t *testing.T) {
	h := &host{}

	if got := h.shim(`C:\Users\p\proj`, nil).execute(nil); got != exitLauncherFailure {
		t.Errorf("exit code = %d, want %d", got, exitLauncherFailure)
	}
	if h.spawned != "" {
		t.Error("nothing should have been spawned")
	}
	assertStderrHasPrefix(t, h.stderr.String())
}

func TestExecuteWarnsAboutDistroConflictButStillRuns(t *testing.T) {
	h := &host{captures: []response{probeOK()}}

	code := h.shim(fakeCWD, []string{envDistro + "=Debian"}).execute(nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, h.stderr.String())
	}

	stderr := h.stderr.String()
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "Debian") {
		t.Errorf("stderr does not warn about the conflict: %q", stderr)
	}
	assertCommandLineContains(t, h.spawned, []string{"-d", "Ubuntu"})
}

// A wsl.exe that predates --cd fails the first probe; the launcher retries
// without it and then routes through a shell to change directory.
func TestExecuteFallsBackWhenWSLHasNoCDFlag(t *testing.T) {
	h := &host{captures: []response{
		{stderr: []byte("Invalid command line argument: --cd"), err: exitError(t, 1)},
		probeOK(),
	}}

	code := h.shim(fakeCWD, nil).execute([]string{"continue"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, h.stderr.String())
	}

	assertCommandLine(t, h.spawned, []string{
		"-d", "Ubuntu", "-e", "/bin/sh", "-c", cdScript, "enclave-wsl-launcher",
		"/home/p/proj", fakeBinary, "continue",
	})
}

func TestExecuteReportsMissingLinuxBinaryWithoutRetrying(t *testing.T) {
	h := &host{captures: []response{{err: exitError(t, probeNotFoundCode)}}}

	if got := h.shim(fakeCWD, nil).execute(nil); got != exitLauncherFailure {
		t.Errorf("exit code = %d, want %d", got, exitLauncherFailure)
	}
	if len(h.commands) != 1 {
		t.Errorf("the launcher retried a conclusive answer: %d calls", len(h.commands))
	}
	if !strings.Contains(h.stderr.String(), "not installed in WSL distribution Ubuntu") {
		t.Errorf("stderr does not name the distribution: %q", h.stderr.String())
	}
}

func TestExecuteListsDistrosWhenTheDistroIsUnusable(t *testing.T) {
	missing := []byte("There is no distribution with the supplied name.")
	h := &host{captures: []response{
		{stderr: missing, err: exitError(t, 1)}, // probe with --cd
		{stderr: missing, err: exitError(t, 1)}, // probe without --cd
		{stdout: utf16LE("Ubuntu-24.04\r\nDebian\r\n", true)},
	}}

	if got := h.shim(fakeCWD, nil).execute(nil); got != exitLauncherFailure {
		t.Errorf("exit code = %d, want %d", got, exitLauncherFailure)
	}
	stderr := h.stderr.String()
	for _, want := range []string{"no distribution with the supplied name", "Ubuntu-24.04", "Debian"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not mention %q: %q", want, stderr)
		}
	}
}

func TestExecuteRejectsANonAbsoluteProbeResult(t *testing.T) {
	h := &host{captures: []response{{stdout: []byte("enclave: command not found\n")}}}

	if got := h.shim(fakeCWD, nil).execute(nil); got != exitLauncherFailure {
		t.Errorf("exit code = %d, want %d", got, exitLauncherFailure)
	}
}

// The probe runs a login shell, so anything /etc/profile.d or ~/.profile prints
// arrives on stdout ahead of the answer. A version manager's banner must not be
// mistaken for the binary's path.
func TestExecuteIgnoresLoginShellChatterInTheProbeOutput(t *testing.T) {
	noisy := "Welcome to Ubuntu 24.04 LTS\nnvm: using node v22.11.0\n" + fakeBinaryOut
	h := &host{captures: []response{{stdout: []byte(noisy)}}}

	code := h.shim(fakeCWD, nil).execute([]string{"continue"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, h.stderr.String())
	}

	assertCommandLine(t, h.spawned, []string{
		"-d", "Ubuntu", "--cd", "/home/p/proj", "-e", fakeBinary, "continue",
	})
}

// A profile that writes without a trailing newline runs into the marker on the
// same line, which is why the marker is searched for rather than the last line
// taken.
func TestExecuteIgnoresProbeChatterWithoutATrailingNewline(t *testing.T) {
	h := &host{captures: []response{{stdout: []byte("direnv: loading" + probeMarker + fakeBinary + "\n")}}}

	if code := h.shim(fakeCWD, nil).execute(nil); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, h.stderr.String())
	}
	assertCommandLineContains(t, h.spawned, []string{fakeBinary})
}

func TestParseProbeOutput(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		want   string
		wantOK bool
	}{
		{"bare answer", "\n" + probeMarker + "/usr/bin/enclave\n", "/usr/bin/enclave", true},
		{"no trailing newline", "\n" + probeMarker + "/usr/bin/enclave", "/usr/bin/enclave", true},
		{"crlf", "\r\n" + probeMarker + "/usr/bin/enclave\r\n", "/usr/bin/enclave", true},
		{"chatter before", "banner\n" + probeMarker + "/usr/bin/enclave\n", "/usr/bin/enclave", true},
		{"marker echoed by the profile", probeMarker + "nonsense\n" + probeMarker + "/usr/bin/enclave\n", "/usr/bin/enclave", true},
		{"no marker", "/usr/bin/enclave\n", "", false},
		{"empty", "", "", false},
		{"relative path", "\n" + probeMarker + "enclave\n", "enclave", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseProbeOutput([]byte(tc.stdout))
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("parseProbeOutput(%q) = %q, %v; want %q, %v", tc.stdout, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestExecutePassesWSLENVToTheChild(t *testing.T) {
	h := &host{captures: []response{probeOK()}}

	code := h.shim(fakeCWD, []string{"ENCLAVE_LOG_LEVEL=debug"}).execute(nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, h.stderr.String())
	}

	value, ok := lookupIn(h.spawnEnv)(wslenvVar)
	if !ok || value != "ENCLAVE_LOG_LEVEL" {
		t.Errorf("child WSLENV = %q, %v", value, ok)
	}
}

func TestExecuteRejectsAnInvalidForwardList(t *testing.T) {
	h := &host{}

	if got := h.shim(fakeCWD, []string{envForwardEnv + "=not a name"}).execute(nil); got != exitLauncherFailure {
		t.Errorf("exit code = %d, want %d", got, exitLauncherFailure)
	}
	if h.spawned != "" {
		t.Error("nothing should have been spawned")
	}
}

func TestExecuteRejectsArgumentsPastTheCommandLineLimit(t *testing.T) {
	h := &host{captures: []response{probeOK()}}

	code := h.shim(fakeCWD, nil).execute([]string{strings.Repeat("a", maxCommandLine)})
	if code != exitLauncherFailure {
		t.Errorf("exit code = %d, want %d", code, exitLauncherFailure)
	}
	if h.spawned != "" {
		t.Error("nothing should have been spawned")
	}
	if !strings.Contains(h.stderr.String(), "too long") {
		t.Errorf("stderr does not explain the cause: %q", h.stderr.String())
	}
}

func TestExitCodeFromRunError(t *testing.T) {
	if code, ok := exitCodeFromRunError(nil); !ok || code != 0 {
		t.Errorf("nil error = %d, %v", code, ok)
	}
	if code, ok := exitCodeFromRunError(exitError(t, 7)); !ok || code != 7 {
		t.Errorf("exit 7 = %d, %v", code, ok)
	}
	// A failure to start is not an exit code and must not be reported as one.
	if _, ok := exitCodeFromRunError(errors.New("file not found")); ok {
		t.Error("a start failure was reported as an exit code")
	}
}

func TestSummarizePrefersWSLDiagnostics(t *testing.T) {
	got := summarize(utf16LE("There is no distribution\r\nwith the supplied name.\r\n", true), errors.New("exit status 1"))
	if got != "There is no distribution; with the supplied name." {
		t.Errorf("summarize = %q", got)
	}

	if got := summarize(nil, errors.New("exit status 1")); got != "exit status 1" {
		t.Errorf("summarize = %q", got)
	}
	if got := summarize(nil, nil); got != "no diagnostic output" {
		t.Errorf("summarize = %q", got)
	}
}

func assertCommandLine(t *testing.T, cmdLine string, wantArgs []string) {
	t.Helper()

	got := parseWindowsCommandLine(cmdLine)
	want := append([]string{fakeWSL}, wantArgs...)
	if len(got) != len(want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func assertCommandLineContains(t *testing.T, cmdLine string, wantArgs []string) {
	t.Helper()

	argv := parseWindowsCommandLine(cmdLine)
	for _, want := range wantArgs {
		found := false
		for _, got := range argv {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("argv %q does not contain %q", argv, want)
		}
	}
}

func assertStderrHasPrefix(t *testing.T, stderr string) {
	t.Helper()

	if !strings.HasPrefix(stderr, errPrefix) {
		t.Errorf("stderr is not attributed to the launcher: %q", stderr)
	}
}
