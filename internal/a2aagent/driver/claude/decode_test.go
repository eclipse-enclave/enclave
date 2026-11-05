// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package claude

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/a2aagent"
)

type decoded struct {
	outcome a2aagent.Outcome
	events  []a2aagent.HarnessEvent
	err     error
	log     string
}

func decodeString(t *testing.T, stream string) decoded {
	t.Helper()
	var log bytes.Buffer
	d := newTestDriver(t, testSpec(), Options{Log: &log})
	var res decoded
	res.outcome, res.err = d.Decode(strings.NewReader(stream), func(ev a2aagent.HarnessEvent) {
		res.events = append(res.events, ev)
	})
	res.log = log.String()
	return res
}

func decodeFixture(t *testing.T, name string) decoded {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return decodeString(t, string(data))
}

func kinds(events []a2aagent.HarnessEvent) []a2aagent.EventKind {
	out := make([]a2aagent.EventKind, len(events))
	for i, ev := range events {
		out[i] = ev.Kind
	}
	return out
}

const p2Session = "f01fbe4e-97ea-48af-9b94-7ad99192c754"

func TestDecodeSuccess(t *testing.T) {
	res := decodeFixture(t, "success")
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.outcome.FinalMessage != "OK" || res.outcome.HarnessSessionID != p2Session {
		t.Fatalf("outcome = %+v", res.outcome)
	}
	wantPaths := TranscriptPaths("/tmp/a2a-p2/work", p2Session)
	if !reflect.DeepEqual(res.outcome.Transcripts, wantPaths) || wantPaths[0] != "projects/-tmp-a2a-p2-work/"+p2Session+".jsonl" {
		t.Fatalf("transcripts = %q", res.outcome.Transcripts)
	}
	want := []a2aagent.HarnessEvent{
		{Kind: a2aagent.EventSession, HarnessSessionID: p2Session},
		{Kind: a2aagent.EventProgress, HarnessSessionID: p2Session, Text: "OK"},
		{Kind: a2aagent.EventFinal, HarnessSessionID: p2Session, Text: "OK"},
	}
	if !reflect.DeepEqual(res.events, want) {
		t.Fatalf("events = %+v", res.events)
	}
	if res.log != "" {
		t.Fatalf("known events were logged: %q", res.log)
	}
}

func TestDecodeToolsAndDenialSkipsKnownEventsQuietly(t *testing.T) {
	res := decodeFixture(t, "tools_and_denial")
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.outcome.FinalMessage == "" || res.log != "" {
		t.Fatalf("final = %q, log = %q", res.outcome.FinalMessage, res.log)
	}
	if got := kinds(res.events); got[0] != a2aagent.EventSession || got[len(got)-1] != a2aagent.EventFinal {
		t.Fatalf("kinds = %v", got)
	}
}

func TestDecodeLastResultIsFinal(t *testing.T) {
	res := decodeFixture(t, "multi_result")
	if res.err != nil {
		t.Fatal(res.err)
	}
	if !strings.HasPrefix(res.outcome.FinalMessage, "The background `sleep 120`") {
		t.Fatalf("final = %q, want the second result", res.outcome.FinalMessage)
	}
	sessions := 0
	finals := 0
	for _, ev := range res.events {
		switch ev.Kind {
		case a2aagent.EventSession:
			sessions++
		case a2aagent.EventFinal:
			finals++
		}
	}
	if sessions != 1 || finals != 1 {
		t.Fatalf("session events = %d, final events = %d", sessions, finals)
	}
}

func TestDecodeTurnStartedAfterLastResultIsNoOutcome(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "multi_result.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	cut := -1
	for i, l := range lines {
		if i > 0 && strings.Contains(l, `"subtype":"init"`) {
			cut = i
		}
	}
	res := decodeString(t, strings.Join(lines[:cut+1], "\n"))
	if !errors.Is(res.err, errNoOutcome) || res.outcome.FinalMessage != "" {
		t.Fatalf("err = %v, outcome = %+v", res.err, res.outcome)
	}
}

func TestDecodeKilledRunsHaveNoOutcome(t *testing.T) {
	for _, name := range []string{"killed_during_tool", "killed_after_init"} {
		t.Run(name, func(t *testing.T) {
			res := decodeFixture(t, name)
			if !errors.Is(res.err, errNoOutcome) {
				t.Fatalf("err = %v", res.err)
			}
			if res.outcome.FinalMessage != "" || res.outcome.HarnessSessionID == "" || len(res.outcome.Transcripts) == 0 {
				t.Fatalf("outcome = %+v", res.outcome)
			}
			for _, ev := range res.events {
				if ev.Kind == a2aagent.EventFinal {
					t.Fatal("final event without a result")
				}
			}
		})
	}
}

func TestDecodeResumeOfUnknownSession(t *testing.T) {
	res := decodeFixture(t, "resume_unknown")
	if res.err == nil || !strings.Contains(res.err.Error(), "No conversation found with session ID") {
		t.Fatalf("err = %v", res.err)
	}
	if res.outcome.HarnessSessionID != "" || len(res.outcome.Transcripts) != 0 || len(res.events) != 0 {
		t.Fatalf("outcome = %+v, events = %+v", res.outcome, res.events)
	}
}

const initLine = `{"type":"system","subtype":"init","cwd":"/w","session_id":"` + fixedSessionID + `"}`

func TestDecodeErrors(t *testing.T) {
	cases := map[string]struct {
		stream string
		want   string
	}{
		"empty stream":       {"", "no system/init"},
		"only rate limit":    {`{"type":"rate_limit_event"}`, "no system/init"},
		"init without id":    {`{"type":"system","subtype":"init"}` + "\n" + `{"type":"result","subtype":"success","result":"x"}`, "system/init without session_id"},
		"result first":       {`{"type":"result","subtype":"success","is_error":false,"result":"x"}`, "result before system/init"},
		"error result":       {initLine + "\n" + `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["boom",{"code":1}]}`, `claude result error_during_execution: boom; {"code":1}`},
		"malformed result":   {initLine + "\n" + `{"type":"result","subtype":"success","result":5}`, "malformed result"},
		"malformed init":     {`{"type":"system","subtype":"init","session_id":7}`, "malformed system/init"},
		"max turns":          {initLine + "\n" + `{"type":"result","subtype":"error_max_turns","is_error":false,"result":"partial"}`, "claude result error_max_turns: partial"},
		"success then error": {initLine + "\n" + `{"type":"result","subtype":"success","result":"x"}` + "\n" + initLine + "\n" + `{"type":"result","subtype":"error_during_execution","is_error":true}`, "claude result error_during_execution"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := decodeString(t, tc.stream)
			if res.err == nil || !strings.Contains(res.err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", res.err, tc.want)
			}
			if res.outcome.FinalMessage != "" {
				t.Fatalf("final = %q on error", res.outcome.FinalMessage)
			}
		})
	}
}

func TestDecodeLogsAndSkipsUnknownEvents(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"hook_response"}`,
		initLine,
		"not json",
		`{"type":"telemetry"}`,
		`{"type":"telemetry"}`,
		`{"type":"system","subtype":"brand_new"}`,
		`{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"content":[{"type":"text","text":"subagent"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"},{"type":"text","text":"a"},{"type":"text","text":"b"}]}}`,
		"",
		`{"type":"result","subtype":"success","is_error":false,"result":"done"}`,
	}, "\r\n")
	res := decodeString(t, stream)
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.outcome.FinalMessage != "done" {
		t.Fatalf("final = %q", res.outcome.FinalMessage)
	}
	for _, want := range []string{"not a JSON object", `unknown event "telemetry"`, `unknown event "system/brand_new"`} {
		if !strings.Contains(res.log, want) {
			t.Errorf("log %q lacks %q", res.log, want)
		}
	}
	if strings.Count(res.log, "telemetry") != 1 || strings.Contains(res.log, "hook_response") {
		t.Errorf("log = %q", res.log)
	}
	var progress []string
	for _, ev := range res.events {
		if ev.Kind == a2aagent.EventProgress {
			progress = append(progress, ev.Text)
		}
	}
	if !reflect.DeepEqual(progress, []string{"a\nb"}) {
		t.Fatalf("progress = %q", progress)
	}
}

func TestDecodeSkipsOversizedLines(t *testing.T) {
	old := maxEventBytes
	maxEventBytes = 256
	t.Cleanup(func() { maxEventBytes = old })
	big := `{"type":"user","message":{"content":"` + strings.Repeat("x", 100<<10) + `"}}`
	res := decodeString(t, initLine+"\n"+big+"\n"+`{"type":"result","subtype":"success","result":"ok"}`)
	if res.err != nil || res.outcome.FinalMessage != "ok" {
		t.Fatalf("err = %v, outcome = %+v", res.err, res.outcome)
	}
	if !strings.Contains(res.log, "over 256 bytes") {
		t.Fatalf("log = %q", res.log)
	}
}

func TestDecodeWithoutTrailingNewlineAndNilEmit(t *testing.T) {
	d := newTestDriver(t, testSpec(), Options{})
	out, err := d.Decode(strings.NewReader(initLine+"\n"+`{"type":"result","subtype":"success","result":"ok"}`), nil)
	if err != nil || out.FinalMessage != "ok" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
}

type failingReader struct{ r io.Reader }

func (f failingReader) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err == io.EOF {
		return n, errors.New("pipe broke")
	}
	return n, err
}

func TestDecodeReadError(t *testing.T) {
	d := newTestDriver(t, testSpec(), Options{})
	out, err := d.Decode(failingReader{strings.NewReader(initLine + "\n")}, nil)
	if err == nil || !strings.Contains(err.Error(), "pipe broke") || out.HarnessSessionID != fixedSessionID {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
}
