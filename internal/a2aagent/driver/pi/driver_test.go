// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package pi

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/a2aagent"
	"enclave/internal/tools"
)

const fixedID = "11111111-2222-4333-8444-555555555555"

func testSpec() tools.A2ASpec {
	return tools.A2ASpec{Command: []string{"pi", "-p", "--mode", "json"}, NewSessionArgs: []string{"--session-id", "{sessionId}"}, ResumeArgs: []string{"--session", "{sessionId}"}, EventFormat: tools.EventFormatPiJSON}
}
func testDriver(t *testing.T, log *bytes.Buffer) *Driver {
	t.Helper()
	opts := Options{}
	if log != nil {
		opts.Log = log
	}
	d, err := New(testSpec(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func TestCommandIsolationAndResume(t *testing.T) {
	d := testDriver(t, nil)
	for _, resume := range []bool{false, true} {
		inv := a2aagent.Invocation{Prompt: "secret prompt", NewHarnessSessionID: fixedID, WorkDir: "/work", Env: []string{"HOME=/home/test", "PI_CODING_AGENT_DIR=/untrusted", "PI_CODING_AGENT_SESSION_DIR=/untrusted"}}
		flag := "--session-id"
		if resume {
			inv.HarnessSessionID = fixedID
			flag = "--session"
		}
		cmd, err := d.Command(context.Background(), inv)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"pi", "-p", "--mode", "json", flag, fixedID, "--session-dir", "/home/test/.pi/agent/a2a-sessions/" + fixedID}
		if !reflect.DeepEqual(cmd.Args, want) || cmd.Dir != "/work" || !reflect.DeepEqual(cmd.Env, inv.Env) {
			t.Fatalf("command: %+v", cmd)
		}
	}
	for _, bad := range []string{"", "-flag", "../escape", "not-uuid"} {
		_, err := d.Command(context.Background(), a2aagent.Invocation{NewHarnessSessionID: bad, Env: []string{"HOME=/home/test"}})
		if err == nil {
			t.Fatalf("accepted ID %q", bad)
		}
	}
	_, err := d.Command(context.Background(), a2aagent.Invocation{NewHarnessSessionID: fixedID, Env: []string{}})
	if err == nil {
		t.Fatal("accepted missing HOME")
	}
}
func TestNewSessionID(t *testing.T) {
	d := testDriver(t, nil)
	a, err := d.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !uuidPattern.MatchString(a) || a[14] != '4' {
		t.Fatalf("IDs %q %q", a, b)
	}
	d.opts.NewSessionID = func() (string, error) { return "../bad", nil }
	if _, err := d.NewSessionID(); err == nil {
		t.Fatal("invalid generated ID accepted")
	}
}

func TestInvocationTranscriptsBeforeHeader(t *testing.T) {
	d := testDriver(t, nil)
	for _, inv := range []a2aagent.Invocation{{NewHarnessSessionID: fixedID}, {HarnessSessionID: fixedID}} {
		if got := d.InvocationTranscripts(inv); !reflect.DeepEqual(got, TranscriptPaths(fixedID)) {
			t.Fatalf("inventory = %q", got)
		}
	}
	if got := d.InvocationTranscripts(a2aagent.Invocation{NewHarnessSessionID: "../bad"}); got != nil {
		t.Fatalf("invalid ID inventory = %q", got)
	}
}
func TestRejectWrongFormat(t *testing.T) {
	spec := testSpec()
	spec.EventFormat = tools.EventFormatClaudeStreamJSON
	if _, err := New(spec, Options{}); err == nil {
		t.Fatal("accepted wrong format")
	}
	spec = testSpec()
	spec.NewSessionArgs = nil
	if _, err := New(spec, Options{}); err == nil {
		t.Fatal("accepted a spec without prechosen session IDs")
	}
}
func TestDecodeFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		fail       bool
	}{
		{"success", "OK", false}, {"resume", "OK", false}, {"tools", "DONE", false}, {"retry", "RECOVERED", false},
		{"killed", "", true}, {"error", "", true}, {"truncated", "", true}, {"aborted", "", true}, {"future", "OK", false},
		{"empty", "", false}, {"malformed-content", "", true}, {"missing-settled-fields", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tc.name+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var log bytes.Buffer
			d := testDriver(t, &log)
			var events []a2aagent.HarnessEvent
			out, err := d.Decode(bytes.NewReader(data), func(e a2aagent.HarnessEvent) { events = append(events, e) })
			if (err != nil) != tc.fail {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if out.FinalMessage != tc.want {
				t.Fatalf("final=%q", out.FinalMessage)
			}
			if out.HarnessSessionID != "" && !reflect.DeepEqual(out.Transcripts, TranscriptPaths(out.HarnessSessionID)) {
				t.Fatalf("transcripts=%q", out.Transcripts)
			}
			if !tc.fail && (len(events) == 0 || events[len(events)-1].Kind != a2aagent.EventFinal) {
				t.Fatalf("events=%+v", events)
			}
			if tc.name == "future" && !strings.Contains(log.String(), "unknown event") {
				t.Fatal("unknown event not logged")
			}
		})
	}
}
func TestDecodeRejectsInvalidSessionAndMalformedOutcome(t *testing.T) {
	d := testDriver(t, nil)
	for _, input := range []string{
		`{ "type":"session", "id":"../../outside" }`,
		`{ "type":"agent_end", "messages":"bad" }`,
		`{ "type":"agent_settled", "aborted":"bad" }`,
		`{ "type":"session", "id":"` + fixedID + `" }` + "\n" + `{"type":"session","id":"00000000-0000-4000-8000-000000000000"}`,
	} {
		if _, err := d.Decode(strings.NewReader(input), nil); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}
