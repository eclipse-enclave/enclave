// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/netlog"
)

func writeNetworkLogFixture(t *testing.T, path string, lines ...string) {
	t.Helper()
	payload := ""
	for _, line := range lines {
		payload += line + "\n"
	}
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestReadNetworkLogEventsReadsTheRotatedGenerationFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.log")
	writeNetworkLogFixture(t, netlog.RotatedPath(path),
		`{"ts":"2026-08-13T11:00:00Z","type":"tcp","verdict":"pass","domain":"old.example"}`)
	writeNetworkLogFixture(t, path,
		`{"ts":"2026-08-13T12:00:00Z","type":"tcp","verdict":"pass","domain":"new.example"}`,
		"Aug 13 12:04:35 dnsmasq[12]: config telemetry.example.com is NXDOMAIN")

	events, skipped, err := readNetworkLogEvents([]networkLogTarget{{label: "demo", path: path}})
	if err != nil {
		t.Fatalf("readNetworkLogEvents() error = %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	if len(events) != 2 || events[0].Domain != "old.example" || events[1].Domain != "new.example" {
		t.Fatalf("events = %+v", events)
	}
}

func TestReadNetworkLogEventsMergesTargetsInTimeOrder(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.log")
	second := filepath.Join(dir, "b.log")
	writeNetworkLogFixture(t, first,
		`{"ts":"2026-08-13T12:00:00Z","type":"tcp","verdict":"pass","domain":"a1.example"}`,
		`{"ts":"2026-08-13T12:00:04Z","type":"tcp","verdict":"pass","domain":"a2.example"}`)
	writeNetworkLogFixture(t, second,
		`{"ts":"2026-08-13T12:00:02Z","type":"tcp","verdict":"pass","domain":"b1.example"}`)

	events, _, err := readNetworkLogEvents([]networkLogTarget{
		{label: "a", path: first},
		{label: "b", path: second},
	})
	if err != nil {
		t.Fatalf("readNetworkLogEvents() error = %v", err)
	}
	want := []string{"a1.example", "b1.example", "a2.example"}
	if len(events) != len(want) {
		t.Fatalf("events = %+v", events)
	}
	for i, event := range events {
		if event.Domain != want[i] {
			t.Fatalf("event %d = %q, want %q", i, event.Domain, want[i])
		}
	}
}

func TestReadNetworkLogEventsMissingFile(t *testing.T) {
	events, skipped, err := readNetworkLogEvents([]networkLogTarget{
		{label: "demo", path: filepath.Join(t.TempDir(), "network.log")},
	})
	if err != nil || len(events) != 0 || skipped != 0 {
		t.Fatalf("got %+v, %d, %v; want an empty read", events, skipped, err)
	}
}

func TestResolveNetworkLogSince(t *testing.T) {
	events := []netlog.Event{
		{Timestamp: "2026-08-13T11:00:00Z", Type: netlog.TypeSession, Verdict: netlog.VerdictInfo, Rule: netlog.RuleSessionStart, Session: "old"},
		{Timestamp: "2026-08-13T11:30:00Z", Type: netlog.TypeTCP, Verdict: netlog.VerdictPass, Domain: "a.example"},
		{Timestamp: "2026-08-13T12:00:00Z", Type: netlog.TypeSession, Verdict: netlog.VerdictInfo, Rule: netlog.RuleSessionStart, Session: "current"},
		{Timestamp: "2026-08-13T12:30:00Z", Type: netlog.TypeTCP, Verdict: netlog.VerdictPass, Domain: "b.example"},
	}

	t.Run("empty", func(t *testing.T) {
		since, err := resolveNetworkLogSince("", events)
		if err != nil || !since.IsZero() {
			t.Fatalf("got %v, %v; want the zero time", since, err)
		}
	})

	t.Run("session resolves to the latest marker", func(t *testing.T) {
		since, err := resolveNetworkLogSince("session", events)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		want := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
		if !since.Equal(want) {
			t.Fatalf("since = %v, want %v", since, want)
		}
	})

	t.Run("session without a marker", func(t *testing.T) {
		if _, err := resolveNetworkLogSince("session", events[1:2]); err == nil {
			t.Fatal("expected an error when the log predates session markers")
		}
	})

	t.Run("duration", func(t *testing.T) {
		before := time.Now().Add(-10 * time.Minute)
		since, err := resolveNetworkLogSince("10m", nil)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if since.Before(before.Add(-time.Minute)) || since.After(time.Now()) {
			t.Fatalf("since = %v, want roughly ten minutes ago", since)
		}
	})

	t.Run("rfc3339", func(t *testing.T) {
		since, err := resolveNetworkLogSince("2026-08-13T12:00:00Z", nil)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if !since.Equal(time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)) {
			t.Fatalf("since = %v", since)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		for _, value := range []string{"yesterday", "-5m", "10x"} {
			if _, err := resolveNetworkLogSince(value, nil); err == nil {
				t.Fatalf("resolveNetworkLogSince(%q) was accepted", value)
			}
		}
	})
}

func TestFilterGatewaysBySession(t *testing.T) {
	gateways := []backend.GatewayInfo{
		{Name: "enclave-demo-claude-gateway", SessionContainer: "enclave-demo-claude", Tool: "claude", ProjectHash: "abc"},
		{Name: "enclave-other-codex-gateway", SessionContainer: "enclave-other-codex", Tool: "codex", ProjectHash: "def"},
	}

	for _, name := range []string{"enclave-demo-claude", "enclave-demo-claude-gateway"} {
		matched := filterGatewaysBySession(gateways, name)
		if len(matched) != 1 || matched[0].Tool != "claude" {
			t.Fatalf("filterGatewaysBySession(%q) = %+v", name, matched)
		}
	}
	if matched := filterGatewaysBySession(gateways, "missing"); len(matched) != 0 {
		t.Fatalf("filterGatewaysBySession(missing) = %+v", matched)
	}
}
