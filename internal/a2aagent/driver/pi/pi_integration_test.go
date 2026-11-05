// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build pi

package pi

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
	"enclave/internal/tools"
	"sigs.k8s.io/yaml"
)

// A real Pi process uses an isolated local OpenAI-compatible endpoint. These
// checks cost no model tokens and never read or refresh host credentials.
func TestRealNewResumeToolsAndMCP(t *testing.T) {
	binary, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi not on PATH")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	for _, p := range []string{home, work, filepath.Join(home, ".pi", "agent")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		toolDone := false
		for _, m := range req.Messages {
			if m.Role == "tool" {
				toolDone = true
			}
		}
		delta := map[string]any{"role": "assistant", "content": "OK"}
		reason := "stop"
		if !toolDone {
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_test", "type": "function", "function": map[string]any{"name": "bash", "arguments": `{"command":"printf tool-ran > marker"}`}}}}
			reason = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for i, v := range []any{delta, map[string]any{}} {
			var finish any
			if i == 1 {
				finish = reason
			}
			chunk := map[string]any{"id": "test", "object": "chat.completion.chunk", "created": 1, "model": "test", "choices": []any{map[string]any{"index": 0, "delta": v, "finish_reason": finish}}}
			data, _ := json.Marshal(chunk)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	models := map[string]any{"providers": map[string]any{"test": map[string]any{"api": "openai-completions", "baseUrl": server.URL + "/v1", "apiKey": "test", "models": []any{map[string]any{"id": "test", "contextWindow": 8192, "maxTokens": 100}}}}}
	data, _ := json.Marshal(models)
	if err := os.WriteFile(filepath.Join(home, ".pi", "agent", "models.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "HOME" || strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "PI_") || strings.HasPrefix(name, "ANTHROPIC") || strings.HasPrefix(name, "OPENAI") || strings.HasPrefix(name, "GEMINI") || strings.HasPrefix(name, "MISTRAL") {
			continue
		}
		env = append(env, kv)
	}
	for _, kv := range []string{"HOME=" + home, "PI_CODING_AGENT_DIR=" + filepath.Join(home, ".pi", "agent"), "PI_OFFLINE=1", "PI_TELEMETRY=0", "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "XDG_STATE_HOME=" + filepath.Join(root, "state"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache")} {
		env = append(env, kv)
	}
	data, err = os.ReadFile(filepath.Join("..", "..", "..", "..", "extensions", "tools", "pi", "spec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Sandbox struct {
			A2A tools.A2ASpec `json:"a2a"`
		} `json:"sandbox"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	spec := doc.Sandbox.A2A
	spec.Command[0] = binary
	d, err := New(spec, Options{ExtraArgs: []string{"--approve", "--provider", "test", "--model", "test", "--no-extensions", "--no-skills", "--no-context-files"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CheckVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	id, err := d.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	run := func(resume string) (a2aagent.Outcome, error) {
		inv := a2aagent.Invocation{NewHarnessSessionID: id, HarnessSessionID: resume, WorkDir: work, Env: env}
		cmd, err := d.Command(context.Background(), inv)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdin = strings.NewReader("Run bash, then say OK")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		timer := time.AfterFunc(30*time.Second, func() { _ = cmd.Process.Kill() })
		defer timer.Stop()
		out, decodeErr := d.Decode(stdout, nil)
		waitErr := cmd.Wait()
		if decodeErr == nil && waitErr != nil {
			return out, fmt.Errorf("exit: %w; %s", waitErr, stderr.String())
		}
		return out, decodeErr
	}
	first, err := run("")
	if err != nil {
		t.Fatal(err)
	}
	if first.HarnessSessionID != id || first.FinalMessage != "OK" {
		t.Fatalf("first=%+v", first)
	}
	if data, err := os.ReadFile(filepath.Join(work, "marker")); err != nil || string(data) != "tool-ran" {
		t.Fatalf("tool data=%q err=%v", data, err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".pi", filepath.FromSlash(first.Transcripts[0])))
	if err != nil || len(entries) == 0 {
		t.Fatalf("retention inventory: %v", err)
	}
	second, err := run(id)
	if err != nil || second.HarnessSessionID != id || second.FinalMessage != "OK" {
		t.Fatalf("resume=%+v err=%v", second, err)
	}
	if _, err := run("00000000-0000-4000-8000-000000000000"); err == nil {
		t.Fatal("unknown resume accepted")
	}

	// Exercise the shipped extension and builtin:mcp loading even with extension
	// discovery disabled, using a fake MCP transport instead of a live peer hub.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(bin, "enclave-a2a-agent")
	// The shim's target is the test executable, not shell-interpolated user input.
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run=TestPiMCPHelper\n"
	if err := os.WriteFile(shim, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "mcp.log")
	env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "ENCLAVE_PI_MCP_HELPER=1", "ENCLAVE_PI_MCP_LOG="+logPath)
	extension, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "extensions", "tools", "pi", "templates", "a2a-mcp.js"))
	if err != nil {
		t.Fatal(err)
	}
	d.opts.ExtraArgs = append(d.opts.ExtraArgs, "--extension", "builtin:mcp", "--extension", extension)
	if _, err := run(id); err != nil {
		t.Fatal(err)
	}
	wire, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wire, []byte("initialize")) || !bytes.Contains(wire, []byte("tools/list")) {
		t.Fatalf("MCP wire: %s", wire)
	}
}

func TestPiMCPHelper(t *testing.T) {
	if os.Getenv("ENCLAVE_PI_MCP_HELPER") != "1" {
		return
	}
	f, err := os.OpenFile(os.Getenv("ENCLAVE_PI_MCP_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(1)
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := decoder.Decode(&req); err != nil {
			break
		}
		_, _ = fmt.Fprintln(f, req.Method)
		if req.ID == nil {
			continue
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "serverInfo": map[string]any{"name": "probe", "version": "1"}, "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "ping", "description": "Returns OK", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	_ = f.Close()
	os.Exit(0)
}
