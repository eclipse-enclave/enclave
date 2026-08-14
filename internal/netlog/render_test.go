// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"strings"
	"testing"
	"time"
)

func renderFixture() []Event {
	return []Event{
		{Timestamp: "2026-08-13T12:04:30.000Z", Type: TypeSession, Verdict: VerdictInfo, Rule: RuleSessionStart, Session: "enclave-demo-claude"},
		{Timestamp: "2026-08-13T12:04:31.412Z", Type: TypeHTTP, Method: "GET", Domain: "api.anthropic.com", Path: "/v1/messages", Port: 443, Status: 200, ResponseSize: 4300, Verdict: VerdictPass, Rule: "allowlist", Session: "enclave-demo-claude"},
		{Timestamp: "2026-08-13T12:04:33.100Z", Type: TypeTCP, Domain: "github.com", Port: 443, Verdict: VerdictPass, Rule: "allowlist", Session: "enclave-demo-claude"},
		{Timestamp: "2026-08-13T12:04:35.882Z", Type: TypeDNS, Domain: "telemetry.example.com", Verdict: VerdictDeny, Rule: "nxdomain", Session: "enclave-demo-claude"},
		{Timestamp: "2026-08-13T12:04:36.200Z", Type: TypeHTTP, Method: "GET", Domain: "evil.test", Path: "/x", Port: 443, Status: 403, Verdict: VerdictDeny, Rule: "secret-injection", Session: "enclave-demo-claude"},
	}
}

func humanOptions() RenderOptions {
	return RenderOptions{Location: time.UTC}
}

func renderEvents(events []Event, opts RenderOptions) string {
	var out strings.Builder
	// A strings.Builder never fails.
	_ = WriteEvents(&out, events, opts)
	return out.String()
}

func TestRenderHumanFormAlignsColumns(t *testing.T) {
	out := renderEvents(renderFixture(), humanOptions())
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// header, blank, then one line per event
	if len(lines) != 6 {
		t.Fatalf("rendered %d lines, want 6:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "SESSION") || !strings.Contains(lines[0], "enclave-demo-claude") {
		t.Fatalf("header = %q", lines[0])
	}
	if !strings.Contains(lines[0], "2 pass") || !strings.Contains(lines[0], "2 deny") {
		t.Fatalf("header should carry the session's verdict counts, got %q", lines[0])
	}
	if lines[1] != "" {
		t.Fatalf("expected a blank line after the header, got %q", lines[1])
	}

	rows := lines[2:]
	want := []string{
		" 12:04:31  ✓  GET   api.anthropic.com        /v1/messages         200  4.2 KB",
		" 12:04:33  ✓  tcp   github.com:443                                     allowlist",
		" 12:04:35  ✗  dns   telemetry.example.com                              nxdomain",
		" 12:04:36  ✗  GET   evil.test                /x                   403  secret-injection",
	}
	for i, row := range rows {
		if row != want[i] {
			t.Fatalf("row %d =\n%q\nwant\n%q", i, row, want[i])
		}
	}

	// Every row starts its detail column at the same offset.
	offset := strings.Index(rows[0], "4.2 KB")
	for _, row := range rows[1:] {
		fields := strings.Fields(row)
		detail := fields[len(fields)-1]
		if strings.LastIndex(row, detail) != offset {
			t.Fatalf("detail column of %q starts at %d, want %d", row, strings.LastIndex(row, detail), offset)
		}
	}
}

func TestRenderHumanFormKeepsVerdictGreppable(t *testing.T) {
	out := renderEvents(renderFixture(), RenderOptions{Color: true, Location: time.UTC})
	if strings.Count(out, glyphDeny) != 2 {
		t.Fatalf("expected two deny glyphs in\n%s", out)
	}
	if !strings.Contains(out, "\x1b[31m") {
		t.Fatal("colour was requested but no red escape was emitted")
	}
	plain := renderEvents(renderFixture(), humanOptions())
	if strings.Contains(plain, "\x1b") {
		t.Fatal("colour was not requested but escapes were emitted")
	}
}

func TestRenderSeparatesSessions(t *testing.T) {
	events := renderFixture()
	events = append(events, Event{
		Timestamp: "2026-08-13T13:00:00.000Z", Type: TypeSession, Verdict: VerdictInfo,
		Rule: RuleSessionStart, Session: "enclave-demo-codex",
	}, Event{
		Timestamp: "2026-08-13T13:00:05.000Z", Type: TypeTCP, Domain: "github.com",
		Port: 443, Verdict: VerdictPass, Rule: "allowlist", Session: "enclave-demo-codex",
	})

	out := renderEvents(events, humanOptions())
	if strings.Count(out, "SESSION") != 2 {
		t.Fatalf("expected a boundary per session in\n%s", out)
	}
	if !strings.Contains(out, "enclave-demo-codex") {
		t.Fatalf("second session boundary missing in\n%s", out)
	}
	if !strings.Contains(out, "1 pass") {
		t.Fatalf("second session should count only its own events:\n%s", out)
	}
}

func TestRenderSessionHeaderWithoutCounts(t *testing.T) {
	marker := Event{Timestamp: "2026-08-13T12:04:30.000Z", Type: TypeSession, Verdict: VerdictInfo, Rule: RuleSessionStart, Session: "enclave-demo-claude"}
	line := renderSessionHeader(marker, 0, 0, humanOptions())
	if strings.Contains(line, "pass") || strings.Contains(line, "deny") {
		t.Fatalf("a follow boundary must not claim counts it does not have: %q", line)
	}
}

func TestRenderSummaryHuman(t *testing.T) {
	out := RenderSummary(Aggregate(renderFixture()), humanOptions())
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.HasPrefix(lines[0], " DOMAIN") {
		t.Fatalf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "api.anthropic.com") || !strings.Contains(lines[1], "4.2 KB") {
		t.Fatalf("first row = %q", lines[1])
	}
	totals := strings.Fields(lines[len(lines)-1])
	if len(totals) != 2 || totals[0] != "2" || totals[1] != "2" {
		t.Fatalf("totals row = %v, want [2 2]", totals)
	}
}
