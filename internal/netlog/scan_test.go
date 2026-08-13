// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanGoldenLog(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "network.log"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	result, err := Scan(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Events) != 6 {
		t.Fatalf("parsed %d events, want 6", len(result.Events))
	}
	// Two raw dnsmasq lines, one truncated JSON line and one bare word.
	if result.Skipped != 4 {
		t.Fatalf("Skipped = %d, want 4", result.Skipped)
	}

	first := result.Events[0]
	if !first.IsSessionMarker() || first.Rule != RuleSessionStart {
		t.Fatalf("first event = %+v, want a session start marker", first)
	}
	if first.Session != "enclave-demo-claude" {
		t.Fatalf("session = %q, want enclave-demo-claude", first.Session)
	}
}

func TestScanReportsEveryUnparseableLine(t *testing.T) {
	input := strings.Join([]string{
		`{"ts":"2026-08-13T12:04:31.412Z","type":"http","verdict":"pass","domain":"a.example"}`,
		"",
		"   ",
		"Aug 13 12:04:35 dnsmasq[12]: config telemetry.example.com is NXDOMAIN",
		`{"ts":"2026-08-13T12:04:36.000Z","type":"htt`,
		`{"type":"http","verdict":"pass"}`,
		`{"ts":"2026-08-13T12:04:37.000Z","verdict":"pass"}`,
		`{"ts":"2026-08-13T12:04:38.000Z","type":"tcp","verdict":"pass","domain":"b.example"}`,
	}, "\n")

	result, err := Scan(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Events) != 2 {
		t.Fatalf("parsed %d events, want 2", len(result.Events))
	}
	if result.Skipped != 4 {
		t.Fatalf("Skipped = %d, want 4 (blank lines are not counted)", result.Skipped)
	}
}

func TestScanTruncatedFinalLineWithoutNewline(t *testing.T) {
	input := `{"ts":"2026-08-13T12:04:31.412Z","type":"http","verdict":"pass","domain":"a.example"}` + "\n" +
		`{"ts":"2026-08-13T12:04:32`

	result, err := Scan(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Events) != 1 || result.Skipped != 1 {
		t.Fatalf("got %d events and %d skipped, want 1 and 1", len(result.Events), result.Skipped)
	}
}

func TestParseLineKeepsEveryField(t *testing.T) {
	line := `{"ts":"2026-08-13T12:04:31.412Z","type":"http","method":"POST","domain":"api.github.com",` +
		`"path":"/graphql","port":443,"status":200,"req_bytes":12,"resp_bytes":18432,` +
		`"content_type":"application/json","verdict":"pass","rule":"allowlist","session":"enclave-demo-claude"}`

	event, ok := ParseLine([]byte(line))
	if !ok {
		t.Fatal("ParseLine() reported a malformed line")
	}
	want := Event{
		Timestamp: "2026-08-13T12:04:31.412Z", Type: TypeHTTP, Method: "POST",
		Domain: "api.github.com", Path: "/graphql", Port: 443, Status: 200,
		RequestSize: 12, ResponseSize: 18432, ContentType: "application/json",
		Verdict: VerdictPass, Rule: "allowlist", Session: "enclave-demo-claude",
	}
	if event != want {
		t.Fatalf("ParseLine() = %+v, want %+v", event, want)
	}
	if _, ok := event.Time(); !ok {
		t.Fatal("Time() failed on an RFC3339 timestamp")
	}
}
