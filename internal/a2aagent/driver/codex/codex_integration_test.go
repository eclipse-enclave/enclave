// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build codex

package codex

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2aagent"
)

func TestRealVersion(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex not on PATH")
	}
	d, err := New(shippedSpec(t), Options{ConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.CheckVersion(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestRealNewContextAndResume is opt-in even with the build tag: it requires
// an explicit auth-file path or API key and never reads the host Codex home.
func TestRealNewContextAndResume(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex not on PATH")
	}
	authFile := os.Getenv("CODEX_A2A_TEST_AUTH_FILE")
	apiKey := os.Getenv("CODEX_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if authFile == "" && apiKey == "" {
		t.Skip("billed test needs CODEX_A2A_TEST_AUTH_FILE or an API key")
	}
	root := t.TempDir()
	configDir, workDir := filepath.Join(root, "codex"), filepath.Join(root, "work")
	for _, dir := range []string{configDir, workDir, filepath.Join(root, "home")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if authFile != "" {
		data, err := os.ReadFile(authFile)
		if err != nil {
			t.Fatal("cannot read test auth file")
		}
		if err := os.WriteFile(filepath.Join(configDir, "auth.json"), data, 0o600); err != nil {
			t.Fatal("cannot copy test auth file")
		}
	}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "HOME" || strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "CODEX") || strings.HasPrefix(name, "OPENAI") || strings.HasPrefix(name, "ENCLAVE_A2A_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "HOME="+filepath.Join(root, "home"), "CODEX_HOME="+configDir,
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_STATE_HOME="+filepath.Join(root, "state"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"), "XDG_DATA_HOME="+filepath.Join(root, "data"))
	if apiKey != "" {
		env = append(env, "CODEX_API_KEY="+apiKey)
	}
	model := os.Getenv("CODEX_A2A_TEST_MODEL")
	if model == "" {
		model = "gpt-6.1-sol"
	}
	d, err := New(shippedSpec(t), Options{ConfigDir: configDir, ExtraArgs: []string{"--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-m", model, "-c", `model_reasoning_effort="low"`}})
	if err != nil {
		t.Fatal(err)
	}
	run := func(id, prompt string) a2aagent.Outcome {
		t.Helper()
		cmd, err := d.Command(context.Background(), a2aagent.Invocation{HarnessSessionID: id, WorkDir: workDir, Env: env})
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdin = strings.NewReader(prompt)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		timer := time.AfterFunc(2*time.Minute, func() { _ = cmd.Process.Kill() })
		defer timer.Stop()
		if err := cmd.Wait(); err != nil {
			t.Fatalf("codex process: %v (stderr omitted; may contain auth diagnostics)", err)
		}
		out, err := d.Decode(&stdout, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Transcripts) == 0 {
			t.Fatal("missing rollout inventory")
		}
		rollout := false
		for _, path := range out.Transcripts {
			rollout = rollout || strings.HasPrefix(path, "sessions/")
			if _, err := os.Stat(filepath.Join(configDir, path)); err != nil {
				t.Fatal(err)
			}
		}
		if !rollout {
			t.Fatal("no rollout in inventory")
		}
		sharedDBs, err := filepath.Glob(filepath.Join(configDir, "*.sqlite*"))
		if err != nil || len(sharedDBs) != 0 {
			t.Fatalf("conversation databases escaped private inventory: %q, %v", sharedDBs, err)
		}
		return out
	}
	first := run("", "Remember ORCHID. Reply exactly OK.")
	second := run(first.HarnessSessionID, "What marker did I ask you to remember? Reply only the marker.")
	if second.HarnessSessionID != first.HarnessSessionID || second.FinalMessage != "ORCHID" {
		t.Fatalf("resume did not retain context: %q", second.FinalMessage)
	}
}
