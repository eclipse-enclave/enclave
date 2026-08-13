// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"testing"
	"time"
)

func TestAggregateCountsBytesAndVerdicts(t *testing.T) {
	events := []Event{
		{Timestamp: "2026-08-13T12:00:00Z", Type: TypeSession, Verdict: VerdictInfo, Rule: RuleSessionStart, Session: "s1"},
		{Timestamp: "2026-08-13T12:01:00Z", Type: TypeHTTP, Domain: "api.github.com", Verdict: VerdictPass, RequestSize: 100, ResponseSize: 900},
		{Timestamp: "2026-08-13T12:02:00Z", Type: TypeHTTP, Domain: "api.github.com", Verdict: VerdictPass, RequestSize: 24, ResponseSize: 124},
		{Timestamp: "2026-08-13T12:03:00Z", Type: TypeHTTP, Domain: "API.GitHub.com", Verdict: VerdictDeny, Rule: "secret-injection"},
		{Timestamp: "2026-08-13T12:04:00Z", Type: TypeDNS, Domain: "telemetry.example.com", Verdict: VerdictDeny, Rule: "nxdomain"},
		{Timestamp: "2026-08-13T12:05:00Z", Type: TypeTCP, Verdict: VerdictPass},
	}

	summary := Aggregate(events)
	if len(summary.Domains) != 2 {
		t.Fatalf("aggregated %d domains, want 2 (markers and domainless events are skipped)", len(summary.Domains))
	}
	if summary.TotalPass != 2 || summary.TotalDeny != 2 {
		t.Fatalf("totals = %d pass / %d deny, want 2 / 2", summary.TotalPass, summary.TotalDeny)
	}

	first := summary.Domains[0]
	if first.Domain != "api.github.com" {
		t.Fatalf("busiest domain = %q", first.Domain)
	}
	if first.Pass != 2 || first.Deny != 1 || first.Total() != 3 {
		t.Fatalf("api.github.com = %d pass / %d deny", first.Pass, first.Deny)
	}
	if first.Sent != 124 || first.Received != 1024 {
		t.Fatalf("api.github.com bytes = %d sent / %d received, want 124 / 1024", first.Sent, first.Received)
	}
	wantFirstSeen := time.Date(2026, 8, 13, 12, 1, 0, 0, time.UTC)
	wantLastSeen := time.Date(2026, 8, 13, 12, 3, 0, 0, time.UTC)
	if !first.FirstSeen.Equal(wantFirstSeen) || !first.LastSeen.Equal(wantLastSeen) {
		t.Fatalf("api.github.com seen %v..%v, want %v..%v", first.FirstSeen, first.LastSeen, wantFirstSeen, wantLastSeen)
	}
}

func TestAggregateDenyOnlyDomainHasNoBytes(t *testing.T) {
	summary := Aggregate([]Event{
		{Timestamp: "2026-08-13T12:04:00Z", Type: TypeDNS, Domain: "telemetry.example.com", Verdict: VerdictDeny, Rule: "nxdomain"},
		{Timestamp: "2026-08-13T12:05:00Z", Type: TypeDNS, Domain: "telemetry.example.com", Verdict: VerdictDeny, Rule: "nxdomain"},
	})
	if len(summary.Domains) != 1 {
		t.Fatalf("aggregated %d domains, want 1", len(summary.Domains))
	}
	entry := summary.Domains[0]
	if entry.Pass != 0 || entry.Deny != 2 || entry.Sent != 0 || entry.Received != 0 {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestAggregateSortsEqualCountsAlphabetically(t *testing.T) {
	summary := Aggregate([]Event{
		{Timestamp: "2026-08-13T12:00:00Z", Type: TypeTCP, Domain: "zulu.example", Verdict: VerdictPass},
		{Timestamp: "2026-08-13T12:00:01Z", Type: TypeTCP, Domain: "alpha.example", Verdict: VerdictPass},
	})
	if summary.Domains[0].Domain != "alpha.example" {
		t.Fatalf("order = %q, %q", summary.Domains[0].Domain, summary.Domains[1].Domain)
	}
}

func TestAggregateIgnoresUnparseableTimestamps(t *testing.T) {
	summary := Aggregate([]Event{
		{Timestamp: "nonsense", Type: TypeTCP, Domain: "a.example", Verdict: VerdictPass},
	})
	if !summary.Domains[0].LastSeen.IsZero() {
		t.Fatalf("LastSeen = %v, want the zero time", summary.Domains[0].LastSeen)
	}
}
