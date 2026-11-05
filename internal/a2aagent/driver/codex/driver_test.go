// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package codex

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"sigs.k8s.io/yaml"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/tools"
)

const fixedSessionID = "11111111-2222-4333-8444-555555555555"

func testSpec() tools.A2ASpec {
	return tools.A2ASpec{Command: []string{"codex", "exec", "--json"}, ResumeArgs: []string{"resume", "{sessionId}"}, EventFormat: tools.EventFormatCodexExecJSON}
}

func shippedSpec(t *testing.T) tools.A2ASpec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "extensions", "tools", "codex", "spec.yaml"))
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
		t.Fatal("shipped Codex spec has no a2a block")
	}
	return *doc.Sandbox.A2A
}

func TestShippedOrchestratorConfig(t *testing.T) {
	spec := shippedSpec(t)
	if _, err := New(spec, Options{ConfigDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	var statements []string
	for i := 0; i < len(spec.OrchestratorArgs); i += 2 {
		if i+1 >= len(spec.OrchestratorArgs) || spec.OrchestratorArgs[i] != "-c" {
			t.Fatal("orchestrator config must use invocation-only -c overrides")
		}
		statements = append(statements, spec.OrchestratorArgs[i+1])
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command     string
			Args        []string
			EnvVars     []string `toml:"env_vars"`
			ToolTimeout int      `toml:"tool_timeout_sec"`
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(strings.Join(statements, "\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	server := cfg.MCPServers["enclave-a2a"]
	if server.Command != "enclave-a2a-agent" || !reflect.DeepEqual(server.Args, []string{"mcp"}) {
		t.Fatalf("MCP server = %+v", server)
	}
	if server.ToolTimeout < 150 {
		t.Fatal("MCP timeout cannot cover the default send wait and grace")
	}
	for _, required := range []string{a2a.EnvRole, a2a.EnvGroup, a2a.EnvGroupGeneration, a2a.EnvMember, a2a.EnvMemberGeneration, a2a.EnvHubHost, a2a.EnvHubPort, a2a.EnvInboundToken, a2a.EnvTableDir, a2a.EnvSendWait, a2a.EnvMaxRequestBytes} {
		found := false
		for _, name := range server.EnvVars {
			found = found || name == required
		}
		if !found {
			t.Errorf("MCP server does not forward %s", required)
		}
	}
}

func testDriver(t *testing.T, opts Options) *Driver {
	t.Helper()
	if opts.ConfigDir == "" {
		opts.ConfigDir = t.TempDir()
	}
	d, err := New(testSpec(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCommand(t *testing.T) {
	d := testDriver(t, Options{ExtraArgs: []string{"--dangerously-bypass-approvals-and-sandbox"}})
	if err := os.MkdirAll(filepath.Join(d.opts.ConfigDir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.opts.ConfigDir, "sessions", "rollout-test-"+fixedSessionID+".jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", fixedSessionID} {
		cmd, err := d.Command(context.Background(), a2aagent.Invocation{Prompt: "secret prompt", HarnessSessionID: id, WorkDir: "/workspace", Env: []string{"HOME=/home/user"}})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"codex", "exec", "--json", "--dangerously-bypass-approvals-and-sandbox"}
		want = append(want, "-c", "sqlite_home="+strconv.Quote(filepath.Join(d.opts.ConfigDir, filepath.FromSlash(d.sqlitePath))))
		if id != "" {
			want = append(want, "resume", id)
		}
		want = append(want, "-")
		wantEnv := []string{"HOME=/home/user", "CODEX_HOME=" + d.opts.ConfigDir, "CODEX_SQLITE_HOME=" + filepath.Join(d.opts.ConfigDir, filepath.FromSlash(d.sqlitePath))}
		if !reflect.DeepEqual(cmd.Args, want) || cmd.Dir != "/workspace" || !reflect.DeepEqual(cmd.Env, wantEnv) || cmd.Stdin != nil {
			t.Fatalf("command = %+v, want args %q", cmd, want)
		}
	}
	for _, bad := range []string{"--last", "../other", "not-a-uuid"} {
		if _, err := d.Command(context.Background(), a2aagent.Invocation{HarnessSessionID: bad}); err == nil {
			t.Fatalf("accepted session ID %q", bad)
		}
	}
	if _, err := d.Command(context.Background(), a2aagent.Invocation{HarnessSessionID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}); err == nil || !strings.Contains(err.Error(), "no codex rollout") {
		t.Fatalf("missing resume transcript accepted: %v", err)
	}
	if id, err := d.NewSessionID(); err != nil || id != "" {
		t.Fatalf("NewSessionID = %q, %v", id, err)
	}
	if d.Name() != tools.EventFormatCodexExecJSON {
		t.Fatal(d.Name())
	}
}

func TestSQLiteInventoryOnFailure(t *testing.T) {
	d := testDriver(t, Options{})
	if _, err := d.Command(context.Background(), a2aagent.Invocation{}); err != nil {
		t.Fatal(err)
	}
	first := d.sqlitePath
	if got := d.InvocationTranscripts(a2aagent.Invocation{}); !reflect.DeepEqual(got, []string{first}) {
		t.Fatalf("pre-spawn SQLite inventory = %q", got)
	}
	out, err := d.Decode(strings.NewReader(""), nil)
	if err == nil || !reflect.DeepEqual(out.Transcripts, []string{first}) {
		t.Fatalf("failure lost SQLite inventory: %+v, %v", out, err)
	}
	if _, err := d.Command(context.Background(), a2aagent.Invocation{}); err != nil {
		t.Fatal(err)
	}
	if first == d.sqlitePath {
		t.Fatal("SQLite directory reused across turns")
	}
}

func TestNewAndVersion(t *testing.T) {
	spec := testSpec()
	spec.NewSessionArgs = []string{"--session-id", "{sessionId}"}
	if _, err := New(spec, Options{}); err == nil {
		t.Fatal("accepted caller-chosen session ID")
	}
	spec = testSpec()
	spec.EventFormat = tools.EventFormatClaudeStreamJSON
	if _, err := New(spec, Options{}); err == nil {
		t.Fatal("accepted another driver's format")
	}
	script := filepath.Join(t.TempDir(), "version")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'codex-cli 0.166.0'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ versions, want string }{{">=0.166.0 <0.167.0", ""}, {">=0.167.0", "outside the supported range"}} {
		spec = testSpec()
		spec.VersionCommand, spec.Versions = []string{script}, tc.versions
		d, err := New(spec, Options{})
		if err != nil {
			t.Fatal(err)
		}
		err = d.CheckVersion(context.Background())
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Fatalf("CheckVersion = %v", err)
		}
	}
	if err := testDriver(t, Options{}).CheckVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptPaths(t *testing.T) {
	root := t.TempDir()
	want := []string{"archived_sessions/rollout-old-" + fixedSessionID + ".jsonl", "sessions/2026/11/05/rollout-new-" + fixedSessionID + ".jsonl", "shell_snapshots/" + fixedSessionID + ".123.sh"}
	for _, path := range append(append([]string{}, want...), "sessions/2026/11/05/rollout-other.jsonl", "shell_snapshots/other.sh", "state_5.sqlite") {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "state_5.sqlite"), filepath.Join(root, "shell_snapshots", fixedSessionID+".evil.sh")); err != nil {
		t.Fatal(err)
	}
	got, err := TranscriptPaths(root, fixedSessionID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, %v", got, err)
	}
	if got, err := TranscriptPaths(root, "../"); err != nil || len(got) != 0 {
		t.Fatalf("invalid ID paths = %q, %v", got, err)
	}
	if got, err := TranscriptPaths(t.TempDir(), fixedSessionID); err != nil || len(got) != 0 {
		t.Fatalf("missing paths = %q, %v", got, err)
	}
}

func TestDecodeRecordedFixtures(t *testing.T) {
	for _, tc := range []struct {
		file, final string
		failed      bool
	}{{"success.jsonl", "OK", false}, {"resume.jsonl", "ORCHID", false}, {"mcp.jsonl", "A2A_OK", false}, {"killed_during_tool.jsonl", "", true}} {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			var events []a2aagent.HarnessEvent
			out, err := testDriver(t, Options{}).Decode(bytes.NewReader(data), func(ev a2aagent.HarnessEvent) { events = append(events, ev) })
			if (err != nil) != tc.failed || out.FinalMessage != tc.final || out.HarnessSessionID != fixedSessionID {
				t.Fatalf("out = %+v, err = %v", out, err)
			}
			if len(events) == 0 || events[0].Kind != a2aagent.EventSession {
				t.Fatalf("events = %+v", events)
			}
			if !tc.failed && events[len(events)-1].Kind != a2aagent.EventFinal {
				t.Fatalf("missing final event: %+v", events)
			}
		})
	}
}

func TestDecodeProtocol(t *testing.T) {
	start := `{"type":"thread.started","thread_id":"` + fixedSessionID + `"}` + "\n" + `{"type":"turn.started"}` + "\n"
	message := `{"type":"item.completed","item":{"type":"agent_message","text":"final"}}` + "\n"
	end := `{"type":"turn.completed"}` + "\n"
	for name, tc := range map[string]struct{ stream, want string }{
		"empty":               {"", "without a completed turn"},
		"missing outcome":     {start + message, "without a completed turn"},
		"missing final":       {start + end, "without a completed turn"},
		"missing session":     {message + end, "outside an open turn"},
		"invalid session":     {`{"type":"thread.started","thread_id":"../x"}`, "valid thread_id"},
		"malformed outcome":   {start + message + `{"type":"turn.failed","error":5}`, "malformed turn.failed"},
		"failed":              {start + `{"type":"turn.failed","error":{"message":"quota exceeded"}}`, "quota exceeded"},
		"new unfinished turn": {start + message + end + `{"type":"turn.started"}`, "without a completed turn"},
		"changed thread":      {start + `{"type":"thread.started","thread_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}`, "thread changed"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := testDriver(t, Options{}).Decode(strings.NewReader(tc.stream), nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || out.FinalMessage != "" {
				t.Fatalf("out = %+v, err = %v, want %q", out, err, tc.want)
			}
		})
	}
	var log bytes.Buffer
	d := testDriver(t, Options{Log: &log})
	unknown := `{"type":"future","item":false}` + "\n"
	stream := start + "not json\n" + unknown + unknown + `{"type":"error","message":"Reconnecting"}` + "\n" + message + end
	out, err := d.Decode(strings.NewReader(stream), nil)
	if err != nil || out.FinalMessage != "final" || strings.Count(log.String(), `unknown event "future"`) != 1 || !strings.Contains(log.String(), "Reconnecting") {
		t.Fatalf("out = %+v, err = %v, log = %s", out, err, log.String())
	}
	_, err = d.Decode(io.MultiReader(strings.NewReader(start+message+end), failingReader{}), nil)
	if err == nil || !strings.Contains(err.Error(), "read codex stream") {
		t.Fatal(err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
