// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build claude

// Tests against the real claude binary: go test -tags claude ./internal/a2aagent/driver/claude/
// Each test gets its own HOME, CLAUDE_CONFIG_DIR and work directory under the
// test's temp dir, so the user's Claude config is never read or written.
// Billed turns need ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN and are
// skipped without one; they use --model haiku and one-word replies.

package claude

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"enclave/internal/a2aagent"
	"enclave/internal/tools"
)

func shippedSpec(t *testing.T) tools.A2ASpec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "extensions", "tools", "claude", "spec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Sandbox struct {
			A2A *tools.A2ASpec `json:"a2a"`
		} `json:"sandbox"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Sandbox.A2A == nil {
		t.Fatal("extensions/tools/claude/spec.yaml has no sandbox.a2a block")
	}
	return *doc.Sandbox.A2A
}

type sandbox struct {
	configDir string
	workDir   string
	env       []string
}

func newSandbox(t *testing.T) sandbox {
	t.Helper()
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}
	root := t.TempDir()
	s := sandbox{configDir: filepath.Join(root, "config"), workDir: filepath.Join(root, "work")}
	home := filepath.Join(root, "home")
	for _, dir := range []string{s.configDir, s.workDir, home} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		// Drop the calling Claude session's variables and anything that points at the real home.
		if strings.HasPrefix(name, "CLAUDE") || strings.HasPrefix(name, "ANTHROPIC") || strings.HasPrefix(name, "XDG_") || name == "HOME" {
			continue
		}
		s.env = append(s.env, kv)
	}
	s.env = append(s.env, "HOME="+home, "CLAUDE_CONFIG_DIR="+s.configDir,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"), "XDG_DATA_HOME="+filepath.Join(home, ".local", "share"))
	return s
}

func (s sandbox) withAuth(t *testing.T) sandbox {
	t.Helper()
	found := false
	for _, name := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if v := os.Getenv(name); v != "" {
			s.env = append(s.env, name+"="+v)
			found = true
		}
	}
	if !found {
		t.Skip("billed turn needs ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN")
	}
	return s
}

func (s sandbox) run(t *testing.T, d *Driver, sessionID, prompt string) (a2aagent.Outcome, []a2aagent.HarnessEvent, error) {
	t.Helper()
	inv := a2aagent.Invocation{Prompt: prompt, HarnessSessionID: sessionID, WorkDir: s.workDir, Env: s.env}
	if sessionID == "" {
		newID, err := d.NewSessionID()
		if err != nil {
			t.Fatal(err)
		}
		inv.NewHarnessSessionID = newID
	}
	cmd, err := d.Command(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = strings.NewReader(prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(2*time.Minute, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	var events []a2aagent.HarnessEvent
	out, decodeErr := d.Decode(stdout, func(ev a2aagent.HarnessEvent) { events = append(events, ev) })
	waitErr := cmd.Wait()
	t.Logf("exit: %v, stderr: %s", waitErr, strings.TrimSpace(stderr.String()))
	return out, events, decodeErr
}

func TestRealVersionIsInShippedRange(t *testing.T) {
	newSandbox(t)
	d, err := New(shippedSpec(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CheckVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRealResumeOfUnknownSessionFails(t *testing.T) {
	s := newSandbox(t)
	d, err := New(shippedSpec(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := s.run(t, d, "00000000-0000-4000-8000-000000000000", "hi")
	if err == nil || out.FinalMessage != "" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	t.Logf("decode error: %v", err)
}

func TestRealNewContextAndResume(t *testing.T) {
	s := newSandbox(t).withAuth(t)
	var log bytes.Buffer
	d, err := New(shippedSpec(t), Options{ExtraArgs: []string{"--model", "haiku"}, Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	first, events, err := s.run(t, d, "", "Reply with exactly the word OK and nothing else.")
	if err != nil {
		t.Fatalf("first turn: %v (log %q)", err, log.String())
	}
	if first.HarnessSessionID == "" || events[0].Kind != a2aagent.EventSession || !strings.Contains(first.FinalMessage, "OK") {
		t.Fatalf("first = %+v, events = %+v", first, events)
	}
	transcript := filepath.Join(s.configDir, filepath.FromSlash(first.Transcripts[0]))
	if _, err := os.Stat(transcript); err != nil {
		t.Fatalf("derived transcript path: %v", err)
	}

	second, _, err := s.run(t, d, first.HarnessSessionID, "Reply with exactly the word AGAIN and nothing else.")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if second.HarnessSessionID != first.HarnessSessionID || !strings.Contains(second.FinalMessage, "AGAIN") {
		t.Fatalf("second = %+v", second)
	}
	if log.Len() > 0 {
		t.Logf("driver log:\n%s", log.String())
	}
}
