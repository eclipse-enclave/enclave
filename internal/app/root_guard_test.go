// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/extinstall"
	"enclave/internal/model"
	"enclave/internal/testutil"
)

// setRunningAsRoot pins the root check. Tests using it must not run in
// parallel: the seam is package state.
func setRunningAsRoot(t *testing.T, root bool) {
	t.Helper()
	previous := runningAsRoot
	runningAsRoot = func() bool { return root }
	t.Cleanup(func() { runningAsRoot = previous })
}

func TestCheckRootGuardIgnoresNonRoot(t *testing.T) {
	setRunningAsRoot(t, false)
	if err := checkRootGuard(false); err != nil {
		t.Fatalf("non-root run refused: %v", err)
	}
}

func TestCheckRootGuardRefusesRoot(t *testing.T) {
	setRunningAsRoot(t, true)
	t.Setenv("SUDO_USER", "")
	err := checkRootGuard(false)
	if err == nil {
		t.Fatal("expected root run to be refused")
	}
	msg := err.Error()
	for _, want := range []string{"--allow-root", model.EnvAllowRoot + "=1", "docker group", "https://docs.docker.com/engine/install/linux-postinstall/", "--backend podman", "Run enclave as a regular user."} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not mention %q", msg, want)
		}
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("refusal must stay on one line for the JSON error field: %q", msg)
	}
}

func TestCheckRootGuardAllowsRootWithWarning(t *testing.T) {
	setRunningAsRoot(t, true)
	var err error
	stdout, stderr := captureOutput(t, func() { err = checkRootGuard(true) })
	if err != nil {
		t.Fatalf("allowed root run refused: %v", err)
	}
	if stdout != "" {
		t.Errorf("warning must not reach stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "running as root") {
		t.Errorf("expected a root warning on stderr, got %q", stderr)
	}
}

func TestRootRefusalNamesSudoUser(t *testing.T) {
	t.Setenv("SUDO_USER", "alice")
	if msg := rootRefusal().Error(); !strings.Contains(msg, "run it as alice without sudo") {
		t.Errorf("expected a sudo hint naming alice, got %q", msg)
	}
	t.Setenv("SUDO_USER", "root")
	if msg := rootRefusal().Error(); strings.Contains(msg, "sudo;") {
		t.Errorf("SUDO_USER=root must not produce a sudo hint, got %q", msg)
	}
}

func TestRootAllowed(t *testing.T) {
	if !rootAllowed(true) {
		t.Error("--allow-root must opt in")
	}
	if err := os.Unsetenv(model.EnvAllowRoot); err != nil {
		t.Fatal(err)
	}
	if rootAllowed(false) {
		t.Errorf("unset %s must not opt in", model.EnvAllowRoot)
	}
	for _, value := range []string{"1", "true", "TRUE", "yes", "on", " 1 "} {
		t.Setenv(model.EnvAllowRoot, value)
		if !rootAllowed(false) {
			t.Errorf("%s=%q should opt in", model.EnvAllowRoot, value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "maybe"} {
		t.Setenv(model.EnvAllowRoot, value)
		if rootAllowed(false) {
			t.Errorf("%s=%q should not opt in", model.EnvAllowRoot, value)
		}
	}
}

func TestRunRefusesRootBeforeWritingState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME does not select the home directory on Windows")
	}
	setRunningAsRoot(t, true)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// User command discovery runs before the guard; give it something to find.
	commandsDir := config.HostCommandsHostDir(home)
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUserScript(t, commandsDir, "deploy", "#!/bin/sh\nexit 0\n")

	for _, args := range [][]string{{"ps"}, {"deploy"}} {
		before := testutil.PinTree(t, home)
		var code int
		stdout, stderr := captureOutput(t, func() { code = Run(args) })
		if code != 1 {
			t.Fatalf("Run(%q): expected exit code 1, got %d", args, code)
		}
		if stdout != "" {
			t.Errorf("Run(%q): refusal must not reach stdout, got %q", args, stdout)
		}
		if !strings.Contains(stderr, "refusing to run as root") {
			t.Errorf("Run(%q): expected the refusal on stderr, got %q", args, stderr)
		}
		testutil.AssertTreeUnchanged(t, home, before)
	}
}

// Help, version, and completion are exempt from the guard, so they must not
// write anything, not even a file they remove again.
func TestRunExemptPathsWriteNothingAsRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME does not select the home directory on Windows")
	}
	setRunningAsRoot(t, true)
	// An on-disk app root takes the completers as far as listing names.
	appRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appRoot, "extensions", "tools")); err != nil {
		t.Skipf("bundled tool extensions not found: %v", err)
	}
	t.Setenv(model.EnvHome, appRoot)
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, args := range [][]string{
		{"--help"},
		{"version"},
		{"--version"},
		{"--allow-root", "version"},
		{"completion", "bash"},
		{"__complete", "run", "--tool", ""},
		{"__complete", "run", "--features", ""},
		{"__complete", "update", ""},
		{"__complete", "tools", "remove", ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			before := testutil.PinTree(t, home)
			var code int
			stdout, _ := captureOutput(t, func() { code = Run(args) })
			if code != 0 {
				t.Errorf("Run(%q) as root returned %d", args, code)
			}
			if slices.Contains(args, "--tool") && !strings.Contains(stdout, "claude") {
				t.Errorf("tool completion did not list claude: %q", stdout)
			}
			testutil.AssertTreeUnchanged(t, home, before)
		})
	}
}

func TestRunRootRefusalKeepsExtensionJSONEnvelope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME does not select the home directory on Windows")
	}
	setRunningAsRoot(t, true)
	home := t.TempDir()
	t.Setenv("HOME", home)
	before := testutil.PinTree(t, home)

	var code int
	stdout, _ := captureOutput(t, func() { code = Run([]string{"tools", "add", "owner/repo", "--yes", "--json"}) })
	if code == 0 {
		t.Fatal("expected a non-zero exit code")
	}
	testutil.AssertTreeUnchanged(t, home, before)
	var envelope struct {
		SchemaVersion string                    `json:"schemaVersion"`
		Results       []extinstall.ActionResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not a result envelope: %v\n%s", err, stdout)
	}
	if len(envelope.Results) != 1 || envelope.Results[0].Action != extinstall.ActionFailed || !strings.Contains(envelope.Results[0].Error, "refusing to run as root") {
		t.Fatalf("expected one failed result carrying the refusal, got %+v", envelope.Results)
	}
}
