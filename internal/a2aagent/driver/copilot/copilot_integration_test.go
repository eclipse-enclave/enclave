// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build copilot

package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2aagent"
)

// go test -tags copilot ./internal/a2aagent/driver/copilot uses the real CLI,
// isolated config and an offline scripted provider. The live GitHub test also
// requires COPILOT_GITHUB_TOKEN; it never imports host login credentials.
type sandbox struct {
	root, work string
	env        []string
}

func newSandbox(t *testing.T) sandbox {
	t.Helper()
	if _, err := exec.LookPath("copilot"); err != nil {
		t.Skip("copilot is not installed")
	}
	root := t.TempDir()
	s := sandbox{root: filepath.Join(root, "config"), work: filepath.Join(root, "work")}
	for _, dir := range []string{s.root, s.work, filepath.Join(root, "home")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "HOME" || strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "COPILOT_") || name == "GH_TOKEN" || name == "GITHUB_TOKEN" {
			continue
		}
		s.env = append(s.env, kv)
	}
	s.env = append(s.env, "HOME="+filepath.Join(root, "home"), "COPILOT_HOME="+s.root,
		"XDG_CONFIG_HOME="+filepath.Join(root, "xdg-config"), "XDG_CACHE_HOME="+filepath.Join(root, "xdg-cache"),
		"XDG_STATE_HOME="+filepath.Join(root, "xdg-state"), "XDG_DATA_HOME="+filepath.Join(root, "xdg-data"))
	probe := exec.Command("copilot", "--no-auto-update", "--version")
	probe.Env = s.env
	out, err := probe.Output()
	if err != nil || !strings.Contains(string(out), "1.0.") {
		t.Skipf("Copilot CLI unavailable: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return s
}

func (s sandbox) run(t *testing.T, sessionID, prompt string) (a2aagent.Outcome, error) {
	t.Helper()
	d, err := New(shippedSpec(t), Options{ExtraArgs: []string{"--yolo", "--model", "gpt-4.1"}})
	if err != nil {
		t.Fatal(err)
	}
	inv := a2aagent.Invocation{Prompt: prompt, WorkDir: s.work, Env: s.env, HarnessSessionID: sessionID}
	if sessionID == "" {
		inv.NewHarnessSessionID, err = d.NewSessionID()
		if err != nil {
			t.Fatal(err)
		}
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
	timer := time.AfterFunc(time.Minute, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	out, decodeErr := d.Decode(stdout, nil)
	waitErr := cmd.Wait()
	if waitErr != nil && decodeErr == nil {
		decodeErr = waitErr
	}
	if decodeErr != nil {
		t.Logf("CLI failure: %v, stderr: %s", waitErr, &stderr)
	}
	return out, decodeErr
}

func TestRealOfflineNewResumeAndRetention(t *testing.T) {
	s := newSandbox(t)
	requests := make(chan string, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream   bool            `json:"stream"`
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- string(request.Messages)
		if !request.Stream {
			http.Error(w, "expected a streaming provider request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"id":"probe","object":"chat.completion.chunk","created":0,"model":"gpt-4.1","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":null}]}`,
			`{"id":"probe","object":"chat.completion.chunk","created":0,"model":"gpt-4.1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`[DONE]`,
		} {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
	}))
	defer provider.Close()
	s.env = append(s.env, "COPILOT_OFFLINE=true", "COPILOT_PROVIDER_BASE_URL="+provider.URL+"/v1", "COPILOT_PROVIDER_TYPE=openai")
	first, err := s.run(t, "", "Reply with exactly OK. Retain marker ENCLAVE_RESUME_MARKER.")
	if err != nil || first.FinalMessage != "OK" || first.HarnessSessionID == "" {
		t.Fatalf("first: %+v, %v", first, err)
	}
	privateHome := filepath.Join(s.root, filepath.FromSlash(first.Transcripts[0]))
	for _, file := range []string{"session-store.db", "session-state/" + first.HarnessSessionID + "/events.jsonl"} {
		if _, err := os.Stat(filepath.Join(privateHome, filepath.FromSlash(file))); err != nil {
			t.Fatalf("retained file %s: %v", file, err)
		}
	}
	<-requests
	second, err := s.run(t, first.HarnessSessionID, "Repeat your previous answer.")
	if err != nil || second.FinalMessage != "OK" || second.HarnessSessionID != first.HarnessSessionID {
		t.Fatalf("resume: %+v, %v", second, err)
	}
	if history := <-requests; !strings.Contains(history, "ENCLAVE_RESUME_MARKER") || !strings.Contains(history, "Repeat your previous answer") {
		t.Fatal("resumed provider request did not include both conversation turns")
	}
	if _, err := os.Stat(filepath.Join(s.root, "session-store.db")); !os.IsNotExist(err) {
		t.Fatalf("shared session database created: %v", err)
	}
	if err := os.RemoveAll(privateHome); err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(t, first.HarnessSessionID, "This session was removed."); err == nil {
		t.Fatal("removed context was silently recreated by --resume")
	}
}

func TestRealGitHubNewContextAndResume(t *testing.T) {
	token := os.Getenv("COPILOT_GITHUB_TOKEN")
	if token == "" {
		t.Skip("live GitHub turn requires COPILOT_GITHUB_TOKEN")
	}
	s := newSandbox(t)
	s.env = append(s.env, "COPILOT_GITHUB_TOKEN="+token)
	first, err := s.run(t, "", "Reply with exactly OK and do not call tools.")
	if err != nil || strings.TrimSpace(first.FinalMessage) != "OK" {
		t.Fatalf("first: %+v, %v", first, err)
	}
	second, err := s.run(t, first.HarnessSessionID, "Repeat your previous answer, without tools.")
	if err != nil || second.HarnessSessionID != first.HarnessSessionID || strings.TrimSpace(second.FinalMessage) != "OK" {
		t.Fatalf("resume: %+v, %v", second, err)
	}
}
