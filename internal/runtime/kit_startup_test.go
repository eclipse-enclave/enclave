// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// startupEnv builds a minimal env slice with PATH and KIT plus any extras.
func startupEnv(t *testing.T, extra map[string]string) []string {
	t.Helper()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"KIT=" + kitInitScript(t),
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func TestRunStartupCommandForeground(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "fg.marker")
	script := `set -e; . "$KIT"; enclave_run_startup_command false touch "$MARKER"`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = startupEnv(t, map[string]string{"MARKER": marker})
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("foreground command did not create marker: %v", err)
	}
}

// TestRunStartupCommandBackgroundIsAsync proves background=true detaches: the
// backgrounded command blocks on a FIFO read, so the function must return
// before the marker is created. A foreground bug would deadlock the FIFO write.
func TestRunStartupCommandBackgroundIsAsync(t *testing.T) {
	dir := t.TempDir()
	gate := filepath.Join(dir, "gate")
	marker := filepath.Join(dir, "bg.marker")

	// The script: create the FIFO, launch the backgrounded command that blocks
	// on reading the gate, then assert the marker is absent, open the gate, wait,
	// and assert the marker now exists. Output PASS on success.
	script := `set -e
. "$KIT"
mkfifo "$GATE"
enclave_run_startup_command true sh -c 'read x < "'"$GATE"'"; touch "'"$MARKER"'"'
# Function returned => command was detached. Marker must not exist yet.
if [ -e "$MARKER" ]; then echo "MARKER_TOO_EARLY"; exit 1; fi
echo go > "$GATE"
wait
if [ ! -e "$MARKER" ]; then echo "MARKER_MISSING"; exit 1; fi
echo PASS`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = startupEnv(t, map[string]string{"GATE": gate, "MARKER": marker})
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("expected PASS, got:\n%s", out)
	}
}

func TestRunStartupCommandForegroundFailureNotFatal(t *testing.T) {
	script := `set -e; . "$KIT"; enclave_run_startup_command false false; echo SURVIVED`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = startupEnv(t, nil)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected exit 0 (failure must not abort under set -e): %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "SURVIVED") {
		t.Fatalf("expected SURVIVED in output, got:\n%s", out)
	}
}

func TestRunStartupCommandNoArgsNoop(t *testing.T) {
	script := `set -e; . "$KIT"; enclave_run_startup_command false; echo OK`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = startupEnv(t, nil)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("no-args call must return 0: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "OK") {
		t.Fatalf("expected OK, got:\n%s", out)
	}
}

// TestApplyStartupCommandsRunsAndSkipsRoot exercises the mikefarah-yq-v4 glue.
// Skips on hosts without mikefarah yq (e.g. kislyuk's jq wrapper).
func TestApplyStartupCommandsRunsAndSkipsRoot(t *testing.T) {
	if !hasMikefarahYq(t) {
		t.Skip("requires mikefarah/yq v4; host has kislyuk yq")
	}
	extDir := t.TempDir()
	work := t.TempDir()
	fileA := filepath.Join(work, "a")
	fileRoot := filepath.Join(work, "root")
	fileC := filepath.Join(work, "c")

	spec := `schemaVersion: "1"
kind: sandbox
name: demo
commands:
  startup:
    - command: "touch ` + fileA + `"
    - command: "touch ` + fileRoot + `"
      user: "0"
    - command: [touch, ` + fileC + `]
`
	if err := os.WriteFile(filepath.Join(extDir, "spec.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	script := `set -e; . "$KIT"; enclave_apply_startup_commands "$EXT"; wait`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = startupEnv(t, map[string]string{"EXT": extDir})
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply startup: %v\n%s", err, out)
	}

	if _, err := os.Stat(fileA); err != nil {
		t.Fatalf("string-form command did not run: %v", err)
	}
	if _, err := os.Stat(fileC); err != nil {
		t.Fatalf("seq-form command did not run: %v", err)
	}
	if _, err := os.Stat(fileRoot); err == nil {
		t.Fatalf("root startup command must be skipped, but %s exists", fileRoot)
	}
}
