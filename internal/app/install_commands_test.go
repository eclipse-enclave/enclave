// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// installExtensionCommandsScript returns the absolute path to the yq-glue
// build script that synthesizes commands.install for features.
func installExtensionCommandsScript(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "runtime-assets", "build-scripts", "install-extension-commands.sh"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return p
}

// runInstallHelper sources common.sh, applies any shell-function overrides
// (id/sudo stubs), then runs the given snippet. It returns combined output and
// the command error. These snippets need neither root nor a real sudo.
func runInstallHelper(t *testing.T, snippet string) (string, error) {
	t.Helper()
	script := `. "$COMMON"
` + snippet
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"COMMON=" + commonShScript(t),
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestRunInstallCommandNonRootRunsDirectly(t *testing.T) {
	// A non-root process runs the argv directly; sudo must never be invoked.
	out, err := runInstallHelper(t, `sudo() { echo SUDOCALLED; }
enclave_run_install_command "" echo HELLO`)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "HELLO") {
		t.Fatalf("expected HELLO, got:\n%s", out)
	}
	if strings.Contains(out, "SUDOCALLED") {
		t.Fatalf("sudo must not be called when not root, got:\n%s", out)
	}
}

func TestRunInstallCommandRootDropsToAgent(t *testing.T) {
	// Fake root + non-root target: privileges are dropped via sudo -u agent.
	out, err := runInstallHelper(t, `id() { printf '0\n'; }
sudo() { printf 'SUDO %s\n' "$*"; }
enclave_run_install_command "agent" echo HI`)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "SUDO") || !strings.Contains(out, "-u agent") {
		t.Fatalf("expected sudo -u agent drop, got:\n%s", out)
	}
}

func TestRunInstallCommandRootTargetRunsDirectly(t *testing.T) {
	// Fake root + root target: the command runs directly (no privilege drop).
	out, err := runInstallHelper(t, `id() { printf '0\n'; }
sudo() { printf 'SUDO %s\n' "$*"; }
enclave_run_install_command "root" echo HI`)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "HI") {
		t.Fatalf("expected HI, got:\n%s", out)
	}
	if strings.Contains(out, "SUDO") {
		t.Fatalf("root target must run directly, got:\n%s", out)
	}
}

func TestRunInstallCommandNoArgvNoop(t *testing.T) {
	out, err := runInstallHelper(t, `enclave_run_install_command "agent"; echo OK`)
	if err != nil {
		t.Fatalf("no-argv call must return 0: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "OK" {
		t.Fatalf("no-argv call must be a silent no-op, got:\n%s", out)
	}
}

func TestIsRootUserMatrix(t *testing.T) {
	cases := map[string]bool{
		"0":     true,
		"root":  true,
		"":      false,
		"agent": false,
		"1000":  false,
	}
	for input, want := range cases {
		out, err := runInstallHelper(t,
			`if enclave_is_root_user "`+input+`"; then echo YES; else echo NO; fi`)
		if err != nil {
			t.Fatalf("is_root_user %q: %v\n%s", input, err, out)
		}
		got := strings.TrimSpace(out) == "YES"
		if got != want {
			t.Fatalf("enclave_is_root_user(%q) = %v, want %v", input, got, want)
		}
	}
}

// TestInstallExtensionCommandsSynthesizesSteps exercises the yq-glue build
// script end to end: a feature declaring commands.install (string-form and
// seq-form) has both steps run at build time. Requires mikefarah/yq v4.
func TestInstallExtensionCommandsSynthesizesSteps(t *testing.T) {
	if !hasMikefarahYq(t) {
		t.Skip("requires mikefarah/yq v4; host has kislyuk yq")
	}
	root := t.TempDir()
	work := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	markerStr := filepath.Join(work, "str.marker")
	markerSeq := filepath.Join(work, "seq.marker")

	spec := `schemaVersion: "1"
kind: mixin
name: cmd-feat
commands:
  install:
    - command: "touch ` + markerStr + `"
    - command: [touch, ` + markerSeq + `]
`
	writeHomeFilesFixture(t, filepath.Join(featuresDir, "cmd-feat", "spec.yaml"), spec, 0o644)

	cmd := exec.Command("bash", installExtensionCommandsScript(t))
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"FEATURES=cmd-feat",
		"ENCLAVE_FEATURES_DIR=" + featuresDir,
		"ENCLAVE_SUDO=recorder-not-used",
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install-extension-commands: %v\n%s", err, out)
	}

	if _, err := os.Stat(markerStr); err != nil {
		t.Fatalf("string-form install command did not run: %v", err)
	}
	if _, err := os.Stat(markerSeq); err != nil {
		t.Fatalf("seq-form install command did not run: %v", err)
	}
}

// TestInstallExtensionCommandsInstallScriptWins verifies install.sh is the
// escape hatch: when a feature ships an executable install.sh, its
// commands.install steps are skipped entirely.
func TestInstallExtensionCommandsInstallScriptWins(t *testing.T) {
	if !hasMikefarahYq(t) {
		t.Skip("requires mikefarah/yq v4; host has kislyuk yq")
	}
	root := t.TempDir()
	work := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	marker := filepath.Join(work, "cmd.marker")

	spec := `schemaVersion: "1"
kind: mixin
name: both-feat
commands:
  install:
    - command: "touch ` + marker + `"
`
	writeHomeFilesFixture(t, filepath.Join(featuresDir, "both-feat", "spec.yaml"), spec, 0o644)
	writeHomeFilesFixture(t, filepath.Join(featuresDir, "both-feat", "install.sh"),
		"#!/usr/bin/env bash\n", 0o755)

	cmd := exec.Command("bash", installExtensionCommandsScript(t))
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"FEATURES=both-feat",
		"ENCLAVE_FEATURES_DIR=" + featuresDir,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install-extension-commands: %v\n%s", err, out)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("install.sh must win; commands.install marker must NOT be created")
	}
}
