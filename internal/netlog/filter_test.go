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

func testEvents(t *testing.T) []Event {
	t.Helper()
	return []Event{
		{Timestamp: "2026-08-13T12:00:00Z", Type: TypeSession, Verdict: VerdictInfo, Rule: RuleSessionStart, Session: "s1"},
		{Timestamp: "2026-08-13T12:01:00Z", Type: TypeHTTP, Method: "GET", Domain: "api.github.com", Verdict: VerdictPass},
		{Timestamp: "2026-08-13T12:02:00Z", Type: TypeTCP, Domain: "github.com", Port: 443, Verdict: VerdictPass},
		{Timestamp: "2026-08-13T12:03:00Z", Type: TypeDNS, Domain: "telemetry.example.com", Verdict: VerdictDeny, Rule: "nxdomain"},
		{Timestamp: "2026-08-13T12:04:00Z", Type: TypeHTTP, Method: "POST", Domain: "evil.test", Verdict: VerdictDeny, Rule: "secret-injection"},
	}
}

func domainsOf(events []Event) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		if event.IsSessionMarker() {
			names = append(names, "<session>")
			continue
		}
		names = append(names, event.Domain)
	}
	return names
}

func TestFilterMatrix(t *testing.T) {
	events := testEvents(t)
	since := time.Date(2026, 8, 13, 12, 2, 0, 0, time.UTC)

	cases := []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"empty keeps markers", Filter{}, []string{"<session>", "api.github.com", "github.com", "telemetry.example.com", "evil.test"}},
		{"verdict deny drops markers", Filter{Verdict: VerdictDeny}, []string{"telemetry.example.com", "evil.test"}},
		{"verdict pass", Filter{Verdict: VerdictPass}, []string{"api.github.com", "github.com"}},
		{"type dns", Filter{Type: TypeDNS}, []string{"telemetry.example.com"}},
		{"exact domain", Filter{Domain: "api.github.com"}, []string{"api.github.com"}},
		{"domain covers subdomains", Filter{Domain: "github.com"}, []string{"api.github.com", "github.com"}},
		{"wildcard domain", Filter{Domain: "*.github.com"}, []string{"api.github.com"}},
		{"since drops older and markers stay", Filter{Since: since}, []string{"github.com", "telemetry.example.com", "evil.test"}},
		{"since with verdict", Filter{Since: since, Verdict: VerdictDeny}, []string{"telemetry.example.com", "evil.test"}},
		{"type and verdict combined", Filter{Type: TypeHTTP, Verdict: VerdictDeny}, []string{"evil.test"}},
		{"domain and verdict exclude each other", Filter{Domain: "github.com", Verdict: VerdictDeny}, []string{}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			filter, err := testCase.filter.Normalize()
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			got := domainsOf(filter.Apply(events))
			if len(got) != len(testCase.want) {
				t.Fatalf("got %v, want %v", got, testCase.want)
			}
			for i := range got {
				if got[i] != testCase.want[i] {
					t.Fatalf("got %v, want %v", got, testCase.want)
				}
			}
		})
	}
}

func TestFilterDropsEventsWithUnparseableTimestampUnderSince(t *testing.T) {
	filter := Filter{Since: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)}
	if filter.Match(Event{Timestamp: "not-a-time", Type: TypeHTTP, Verdict: VerdictPass}) {
		t.Fatal("event with an unparseable timestamp matched a --since filter")
	}
}

func TestFilterNormalizeRejectsBadValues(t *testing.T) {
	cases := map[string]Filter{
		"verdict": {Verdict: "maybe"},
		"type":    {Type: "udp"},
		"domain":  {Domain: "https://example.com/x"},
	}
	for name, filter := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := filter.Normalize(); err == nil {
				t.Fatalf("Normalize() accepted %+v", filter)
			}
		})
	}
}

func TestFilterNormalizeIsCaseInsensitive(t *testing.T) {
	filter, err := Filter{Verdict: "DENY", Type: "DNS", Domain: "Telemetry.Example.COM"}.Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	matched := filter.Apply(testEvents(t))
	if len(matched) != 1 || matched[0].Domain != "telemetry.example.com" {
		t.Fatalf("matched %v, want the single dns deny", domainsOf(matched))
	}
}
