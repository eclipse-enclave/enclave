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

// runA2AEntrypoint runs the A2A variant's ENTRYPOINT wrapper in front of the
// real entrypoint.sh, with a main command that records it ran and a fake
// enclave-a2a-agent.
func runA2AEntrypoint(t *testing.T, role string) (home string, output string, err error) {
	t.Helper()
	home = t.TempDir()
	projectDir := filepath.Join(home, "project")
	fakeBin := filepath.Join(home, "bin")
	for _, dir := range []string{projectDir, fakeBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	agent := "#!/bin/sh\n{ printf 'args=%s\\n' \"$*\"; printf 'member=%s\\n' \"$ENCLAVE_A2A_MEMBER\"; printf 'project=%s\\n' \"$PROJECT_DIR\"; } > \"$HOME/agent.out\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "enclave-a2a-agent"), []byte(agent), 0o755); err != nil {
		t.Fatal(err)
	}
	entrypoint, err := filepath.Abs(filepath.Join("..", "..", "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join("..", "..", "runtime-assets", "a2a-agent", "enclave-a2a-entrypoint")
	cmd := exec.Command("sh", wrapper, "sh", "-c", `printf ran > "$HOME/main.out"`)
	cmd.Env = []string{
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home,
		"PROJECT_DIR=" + projectDir,
		"TOOL=claude",
		"ENCLAVE_A2A_MEMBER=builder",
		"ENCLAVE_A2A_ENTRYPOINT=" + entrypoint,
	}
	if role != "" {
		cmd.Env = append(cmd.Env, "ENCLAVE_A2A_ROLE="+role)
	}
	out, err := cmd.CombinedOutput()
	return home, string(out), err
}

func TestA2AEntrypointWorkerRunsAdapterThroughEntrypoint(t *testing.T) {
	t.Parallel()
	home, out, err := runA2AEntrypoint(t, "worker")
	if err != nil {
		t.Fatalf("wrapper: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(home, "agent.out"))
	if err != nil {
		t.Fatalf("adapter did not run: %v\n%s", err, out)
	}
	want := "args=serve\nmember=builder\nproject=" + filepath.Join(home, "project") + "\n"
	if string(got) != want {
		t.Fatalf("adapter saw %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(home, "main.out")); !os.IsNotExist(err) {
		t.Fatalf("the interactive command ran next to the adapter: %v", err)
	}
}

func TestA2AEntrypointRunsMainCommandWithoutWorkerRole(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"", "orchestrator"} {
		home, out, err := runA2AEntrypoint(t, role)
		if err != nil {
			t.Fatalf("role %q: %v\n%s", role, err, out)
		}
		if _, err := os.Stat(filepath.Join(home, "main.out")); err != nil {
			t.Fatalf("role %q: main command did not run: %v", role, err)
		}
		if _, err := os.Stat(filepath.Join(home, "agent.out")); !os.IsNotExist(err) {
			t.Fatalf("role %q: adapter ran: %v", role, err)
		}
	}
}

func TestA2AEntrypointRejectsUnknownRole(t *testing.T) {
	t.Parallel()
	home, out, err := runA2AEntrypoint(t, "host")
	if err == nil || !strings.Contains(out, "Unknown ENCLAVE_A2A_ROLE: host") {
		t.Fatalf("err = %v, output %q", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, "main.out")); !os.IsNotExist(err) {
		t.Fatal("main command ran")
	}
}
