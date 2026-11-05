// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"enclave/internal/a2aagent"
	"enclave/internal/tools"
)

const testID = "11111111-1111-4111-8111-111111111111"

func shippedSpec(t *testing.T) tools.A2ASpec {
	t.Helper()
	data, err := os.ReadFile("../../../../extensions/tools/copilot/spec.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Sandbox struct {
			A2A *tools.A2ASpec `json:"a2a"`
		} `json:"sandbox"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Sandbox.A2A == nil {
		t.Fatalf("shipped spec: %v", err)
	}
	return *doc.Sandbox.A2A
}

func newDriver(t *testing.T, log *bytes.Buffer) *Driver {
	t.Helper()
	opts := Options{}
	if log != nil {
		opts.Log = log
	}
	d, err := New(shippedSpec(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCommandIsolatesEverySessionDatabaseAndKeepsConfig(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"config.json", "settings.json", "session-store.db"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := New(shippedSpec(t), Options{ExtraArgs: []string{"--yolo"}})
	if err != nil {
		t.Fatal(err)
	}
	inv := a2aagent.Invocation{Prompt: "PRIVATE PROMPT", NewHarnessSessionID: testID, WorkDir: t.TempDir(), Env: []string{"COPILOT_HOME=" + root, "COPILOT_GITHUB_TOKEN=fake"}}
	cmd, err := d.Command(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	wantHome := filepath.Join(root, "a2a-harness", "copilot", testID)
	if envValue(cmd.Env, "COPILOT_HOME") != wantHome || envValue(cmd.Env, "COPILOT_GITHUB_TOKEN") != "fake" {
		t.Fatalf("wrong environment: %v", cmd.Env)
	}
	if cmd.Dir != inv.WorkDir || cmd.Stdin != nil || strings.Contains(strings.Join(cmd.Args, " "), inv.Prompt) {
		t.Fatalf("prompt or workdir contract: %+v", cmd)
	}
	wantArgs := append(append(append([]string{}, d.spec.Command...), "--yolo"), "--session-id", testID)
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("argv: %v, want %v", cmd.Args, wantArgs)
	}
	for _, name := range []string{"config.json", "settings.json", "skills"} {
		target, err := os.Readlink(filepath.Join(wantHome, name))
		if err != nil || target != filepath.Join(root, name) {
			t.Fatalf("configuration inheritance %s: %q, %v", name, target, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(wantHome, "session-store.db")); !os.IsNotExist(err) {
		t.Fatalf("shared history database inherited: %v", err)
	}
	inv.HarnessSessionID, inv.NewHarnessSessionID = testID, ""
	resumed, err := d.Command(context.Background(), inv)
	if err != nil || !reflect.DeepEqual(resumed.Args[len(resumed.Args)-2:], []string{"--resume", testID}) {
		t.Fatalf("resume: %v, %v", resumed, err)
	}
	if err := os.RemoveAll(wantHome); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "settings.json")); err != nil {
		t.Fatalf("retention followed a config symlink: %v", err)
	}
}

func TestSessionIDAndCommandValidation(t *testing.T) {
	d := newDriver(t, nil)
	id, err := d.NewSessionID()
	if err != nil || !uuidPattern.MatchString(id) {
		t.Fatalf("id=%q, err=%v", id, err)
	}
	for _, id := range []string{"", "../../secret", "--evil"} {
		if _, err := d.Command(context.Background(), a2aagent.Invocation{NewHarnessSessionID: id}); err == nil {
			t.Fatalf("accepted ID %q", id)
		}
	}
	if TranscriptPaths("../../secret") != nil {
		t.Fatal("unsafe transcript path")
	}
	cmd, err := d.Command(context.Background(), a2aagent.Invocation{NewHarnessSessionID: testID, Env: []string{"HOME=" + t.TempDir()}})
	if err != nil || !strings.Contains(envValue(cmd.Env, "COPILOT_HOME"), "/.copilot/a2a-harness/copilot/") {
		t.Fatalf("HOME fallback: %v, %v", cmd, err)
	}
}

func TestDecodeRecordedCLIStreams(t *testing.T) {
	for _, name := range []string{"success", "resume", "cancel"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", name+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var events []a2aagent.HarnessEvent
			out, err := newDriver(t, nil).Decode(bytes.NewReader(data), func(ev a2aagent.HarnessEvent) { events = append(events, ev) })
			if name == "cancel" {
				if err == nil || out.FinalMessage != "" {
					t.Fatalf("interrupted stream completed: %+v, %v", out, err)
				}
				return
			}
			if err != nil || out.FinalMessage != "OK" || out.HarnessSessionID != testID || !reflect.DeepEqual(out.Transcripts, TranscriptPaths(testID)) {
				t.Fatalf("out=%+v, err=%v", out, err)
			}
			if events[len(events)-1].Kind != a2aagent.EventFinal {
				t.Fatalf("no final event: %+v", events)
			}
		})
	}
}

func TestDecodeFinalityAndFailures(t *testing.T) {
	message := `{"type":"assistant.message","data":{"content":"OK","toolRequests":[]}}` + "\n"
	result := `{"type":"result","sessionId":"` + testID + `","exitCode":0}` + "\n"
	for _, tc := range []struct {
		name, stream, want string
		fail               bool
	}{
		{"success", message + result, "OK", false},
		{"empty reply", strings.ReplaceAll(message, "OK", "") + result, "", false},
		{"unknown", `{"type":"future.event"}` + "\n" + message + result, "OK", false},
		{"subagent", message + `{"type":"assistant.message","agentId":"child","data":{"content":"WRONG"}}` + "\n" + result, "OK", false},
		{"old subagent", message + `{"type":"assistant.message","data":{"content":"WRONG","parentToolCallId":"child"}}` + "\n" + result, "OK", false},
		{"missing result", message, "", true},
		{"missing message", result, "", true},
		{"tool not final", `{"type":"assistant.message","data":{"content":"working","toolRequests":[{}]}}` + "\n" + result, "", true},
		{"exit error", message + strings.ReplaceAll(result, `"exitCode":0`, `"exitCode":1`), "", true},
		{"missing exit", message + `{"type":"result","sessionId":"` + testID + `"}`, "", true},
		{"unsafe session", message + strings.ReplaceAll(result, testID, "../outside"), "", true},
		{"session error", message + `{"type":"session.error","data":{"message":"provider unavailable"}}` + "\n" + result, "", true},
		{"malformed message", `{"type":"assistant.message","data":{"content":4}}` + "\n" + result, "", true},
		{"turn after result", message + result + `{"type":"assistant.turn_start","data":{}}`, "", true},
		{"message after result", message + result + message, "", true},
		{"malformed last result", message + result + strings.ReplaceAll(result, `"exitCode":0`, `"exitCode":"bad"`), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := newDriver(t, nil).Decode(strings.NewReader(tc.stream), nil)
			if (err != nil) != tc.fail || out.FinalMessage != tc.want {
				t.Fatalf("out=%+v err=%v", out, err)
			}
		})
	}
}

func TestUnknownEventsLoggedOnce(t *testing.T) {
	var log bytes.Buffer
	d := newDriver(t, &log)
	_, _ = d.Decode(strings.NewReader("{\"type\":\"future\"}\n{\"type\":\"future\"}\n"), nil)
	if strings.Count(log.String(), "future") != 1 {
		t.Fatalf("log: %s", &log)
	}
}

func TestInvocationInventoryAndMCPTemplate(t *testing.T) {
	d := newDriver(t, nil)
	for _, inv := range []a2aagent.Invocation{{NewHarnessSessionID: testID}, {HarnessSessionID: testID}} {
		if !reflect.DeepEqual(d.InvocationTranscripts(inv), TranscriptPaths(testID)) {
			t.Fatalf("missing pre-spawn inventory for %+v", inv)
		}
	}
	if len(d.InvocationTranscripts(a2aagent.Invocation{})) != 0 {
		t.Fatal("inventoried invalid ID")
	}
	if !reflect.DeepEqual(d.spec.OrchestratorArgs, []string{"--additional-mcp-config", "@/usr/local/share/enclave/templates/copilot-a2a-mcp.json"}) {
		t.Fatalf("unexpected orchestrator wiring: %v", d.spec.OrchestratorArgs)
	}
	data, err := os.ReadFile("../../../../extensions/tools/copilot/templates/a2a-mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Servers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Tools   []string `json:"tools"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	server := config.Servers["enclave-a2a"]
	if len(config.Servers) != 1 || server.Type != "local" || server.Command != "enclave-a2a-agent" || !reflect.DeepEqual(server.Args, []string{"mcp"}) || !reflect.DeepEqual(server.Tools, []string{"*"}) {
		t.Fatalf("unexpected MCP config: %+v", config)
	}
}

func TestVersionGateAndWrongFormat(t *testing.T) {
	spec := shippedSpec(t)
	spec.EventFormat = tools.EventFormatClaudeStreamJSON
	if _, err := New(spec, Options{}); err == nil {
		t.Fatal("accepted another driver's event format")
	}
	spec = shippedSpec(t)
	for _, tc := range []struct {
		version string
		fail    bool
	}{{"1.0.104", true}, {"1.0.105", false}, {"1.1.0", true}} {
		spec.VersionCommand = []string{"sh", "-c", "printf 'Copilot CLI " + tc.version + "\\n'"}
		d, err := New(spec, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.CheckVersion(context.Background()); (err != nil) != tc.fail {
			t.Fatalf("version %s: %v", tc.version, err)
		}
	}
}
