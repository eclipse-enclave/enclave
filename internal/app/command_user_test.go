// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/usercmd"
)

func writeUserScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatalf("write script: %v", err)
	}
	return path
}

// captureOutput swaps os.Stdout/os.Stderr around fn so its output (and any
// logx message) can be asserted.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	// Drain while fn runs: output larger than the pipe buffer, which shrinks
	// to a few pages once the user exceeds fs.pipe-user-pages-soft, would
	// otherwise block fn forever.
	outCh := drainPipe(outR)
	errCh := drainPipe(errR)
	os.Stdout, os.Stderr = outW, errW
	fn()
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = origOut, origErr

	outRes, errRes := <-outCh, <-errCh
	if outRes.err != nil {
		t.Fatalf("read stdout: %v", outRes.err)
	}
	if errRes.err != nil {
		t.Fatalf("read stderr: %v", errRes.err)
	}
	return string(outRes.data), string(errRes.data)
}

type pipeResult struct {
	data []byte
	err  error
}

func drainPipe(r *os.File) <-chan pipeResult {
	ch := make(chan pipeResult, 1)
	go func() {
		data, err := io.ReadAll(r)
		_ = r.Close()
		ch <- pipeResult{data: data, err: err}
	}()
	return ch
}

func runHostCommandCaptured(t *testing.T, cmd usercmd.Command, args []string, projectDir, home string, allowRoot bool) (stdout, stderr string, code int) {
	t.Helper()
	stdout, stderr = captureOutput(t, func() {
		code = runUserHostCommand(cmd, args, projectDir, home, allowRoot)
	})
	return stdout, stderr, code
}

func TestRunUserHostCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixtures require a POSIX shell")
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := t.TempDir()
	script := writeUserScript(t, dir, "deploy", "#!/bin/sh\n"+
		"echo \"args:$*\"\n"+
		"echo \"bin:$ENCLAVE_BIN\"\n"+
		"echo \"root:$ENCLAVE_PROJECT_ROOT\"\n"+
		"echo \"cfg:$ENCLAVE_CONFIG_DIR\"\n"+
		"exit 7\n")
	cmd := usercmd.Command{Name: "deploy", Path: script, Target: usercmd.TargetHost}

	stdout, _, code := runHostCommandCaptured(t, cmd, []string{"--env", "prod"}, "/tmp/project", "/home/user", false)

	if code != 7 {
		t.Fatalf("expected exit code 7, got %d", code)
	}
	for _, want := range []string{
		"args:--env prod",
		"root:/tmp/project",
		"cfg:/home/user/.config/enclave",
		"bin:/",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected stdout to contain %q, got:\n%s", want, stdout)
		}
	}
}

func TestRunUserHostCommandStartFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cmd := usercmd.Command{Name: "ghost", Path: missing, Target: usercmd.TargetHost}

	_, stderr, code := runHostCommandCaptured(t, cmd, nil, "/tmp/project", "/home/user", false)

	if code != 1 {
		t.Fatalf("expected exit code 1 for start failure, got %d", code)
	}
	if !strings.Contains(stderr, missing) {
		t.Fatalf("expected error to name the script path %q, got:\n%s", missing, stderr)
	}
}

func TestRunUserHostCommandForwardsRootOptIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixtures require a POSIX shell")
	}
	t.Setenv(model.EnvAllowRoot, "")
	script := writeUserScript(t, t.TempDir(), "deploy", "#!/bin/sh\necho \"allow:$"+model.EnvAllowRoot+"\"\n")
	cmd := usercmd.Command{Name: "deploy", Path: script, Target: usercmd.TargetHost}

	for _, tc := range []struct {
		allowRoot bool
		want      string
	}{
		{allowRoot: true, want: "allow:1\n"},
		{allowRoot: false, want: "allow:\n"},
	} {
		stdout, _, code := runHostCommandCaptured(t, cmd, nil, "/tmp/project", "/home/user", tc.allowRoot)
		if code != 0 {
			t.Fatalf("allowRoot=%v: expected exit code 0, got %d", tc.allowRoot, code)
		}
		if stdout != tc.want {
			t.Fatalf("allowRoot=%v: expected %q, got %q", tc.allowRoot, tc.want, stdout)
		}
	}
}

func TestPrepareUserSessionCommand(t *testing.T) {
	home := "/home/user"
	uc := usercmd.Command{Name: "triage", Path: "/p/triage", Target: usercmd.TargetSession}
	parsed := cli.Result{
		Action:          "user-command",
		UserCommand:     &uc,
		UserCommandArgs: []string{"--env", "prod", "--", "-x"},
	}

	mount := prepareUserSessionCommand(&parsed, home)

	if parsed.Action != "shell" {
		t.Fatalf("expected action shell, got %q", parsed.Action)
	}
	if !parsed.Options.Shell {
		t.Fatalf("expected Shell to be set")
	}
	containerPath := model.UserCommandsContainerDir + "/triage"
	wantArgs := []string{"-c", `exec "$@"`, "triage", containerPath, "--env", "prod", "--", "-x"}
	if !reflect.DeepEqual(parsed.Options.CmdArgs, wantArgs) {
		t.Fatalf("unexpected CmdArgs:\n got %#v\nwant %#v", parsed.Options.CmdArgs, wantArgs)
	}
	if mount == nil {
		t.Fatalf("expected a user command mount")
		return
	}
	if mount.HostDir != config.HostCommandsSessionDir(home) {
		t.Fatalf("expected host dir %q, got %q", config.HostCommandsSessionDir(home), mount.HostDir)
	}
	if mount.ContainerPath != model.UserCommandsContainerDir {
		t.Fatalf("expected container path %q, got %q", model.UserCommandsContainerDir, mount.ContainerPath)
	}
	// The host command tree must never be referenced by the session mount.
	if strings.Contains(mount.HostDir, filepath.Join("commands", "host")) {
		t.Fatalf("session mount must not reference the host command tree: %q", mount.HostDir)
	}
}
