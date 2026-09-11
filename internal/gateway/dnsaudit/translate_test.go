// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package dnsaudit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/netlog"
)

const session = "enclave-demo-claude"

var stamp = time.Date(2026, 8, 13, 12, 57, 32, 0, time.UTC)

// translateFile runs the parser over a fixture the way Run does.
func translateFile(t *testing.T, name string) []netlog.Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var events []netlog.Event
	for _, line := range strings.Split(string(raw), "\n") {
		if event, ok := ParseLine(line, stamp, session); ok {
			events = append(events, event)
		}
	}
	return events
}

// testdata/dnsmasq.log is real `dnsmasq --log-queries` output captured from a
// gateway container (dnsmasq 2.91), including a blocked domain and the routine
// NODATA-IPv6 answers an allowlisted host produces.
func TestParseCapturedGatewayLog(t *testing.T) {
	events := translateFile(t, "dnsmasq.log")
	if len(events) != 4 {
		t.Fatalf("translated %d events, want 4:\n%+v", len(events), events)
	}
	for _, event := range events {
		if event.Type != netlog.TypeDNS || event.Verdict != netlog.VerdictDeny {
			t.Fatalf("event = %+v, want a dns deny", event)
		}
		if event.Rule != "nxdomain" {
			t.Fatalf("rule = %q, want nxdomain for a blackholed domain", event.Rule)
		}
		if event.Session != session {
			t.Fatalf("session = %q, want %q", event.Session, session)
		}
		if event.Timestamp != stamp.Format(netlog.TimeFormat) {
			t.Fatalf("timestamp = %q", event.Timestamp)
		}
	}
	if events[0].Domain != "telemetry.example.com" || events[2].Domain != "does-not-exist.invalid" {
		t.Fatalf("domains = %q, %q", events[0].Domain, events[2].Domain)
	}
}

// NODATA is an answer, not a denial: dnsmasq returns NODATA-IPv6 for the AAAA
// lookup of every allowlisted host without an IPv6 record. Recording it would
// report allowed traffic as denied.
func TestParseIgnoresNodataForAllowedDomains(t *testing.T) {
	for _, event := range translateFile(t, "dnsmasq.log") {
		if event.Domain == "github.com" {
			t.Fatalf("NODATA-IPv6 for an allowed domain produced %+v", event)
		}
	}
}

// testdata/dnsmasq-upstream-failures.log is constructed in dnsmasq's observed
// "<source> <name> is <status>" shape; the captured session contained no
// upstream failure to copy.
func TestParseUpstreamFailures(t *testing.T) {
	events := translateFile(t, "dnsmasq-upstream-failures.log")
	want := []struct {
		domain string
		rule   string
	}{
		{"broken.example.com", "upstream-servfail"},
		{"blocked.example.com", "upstream-refused"},
		{"gone.example.com", "upstream-nxdomain"},
	}
	if len(events) != len(want) {
		t.Fatalf("translated %d events, want %d:\n%+v", len(events), len(want), events)
	}
	for i, event := range events {
		if event.Domain != want[i].domain || event.Rule != want[i].rule {
			t.Fatalf("event %d = %+v, want %s/%s", i, event, want[i].domain, want[i].rule)
		}
	}
}

func TestParseLineRejectsNonDenials(t *testing.T) {
	cases := []string{
		"",
		"Aug 13 12:57:14 dnsmasq[455]: query[A] api.anthropic.com from 127.0.0.1",
		"Aug 13 12:57:14 dnsmasq[455]: forwarded api.anthropic.com to 8.8.8.8",
		"Aug 13 12:57:14 dnsmasq[455]: reply api.anthropic.com is 160.79.104.10",
		"Aug 13 12:57:14 dnsmasq[455]: cached github.com is NODATA-IPv6",
		"Aug 13 12:57:31 dnsmasq[455]: /etc/hosts localhost is 127.0.0.1",
		"Aug 13 12:57:13 dnsmasq[455]: using nameserver 8.8.8.8#53 for domain github.com",
		"Aug 13 12:57:13 dnsmasq[455]: started, version 2.91 cachesize 150",
		`{"ts":"2026-08-13T12:04:31.412Z","type":"http","verdict":"pass"}`,
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			if event, ok := ParseLine(line, stamp, session); ok {
				t.Fatalf("line produced %+v", event)
			}
		})
	}
}

func TestParseLineNormalizesTheDomain(t *testing.T) {
	event, ok := ParseLine("Aug 13 12:57:32 dnsmasq[455]: config Telemetry.Example.COM. is NXDOMAIN", stamp, session)
	if !ok {
		t.Fatal("ParseLine() rejected a denial")
	}
	if event.Domain != "telemetry.example.com" {
		t.Fatalf("domain = %q", event.Domain)
	}
}

func TestRunAppendsEventsToTheNetworkLog(t *testing.T) {
	dir := t.TempDir()
	dnsmasqLog := filepath.Join(dir, "dnsmasq.log")
	networkLog := filepath.Join(dir, "network.log")
	if err := os.WriteFile(dnsmasqLog, []byte("Aug 13 12:57:32 dnsmasq[455]: config telemetry.example.com is NXDOMAIN\n"), 0o600); err != nil {
		t.Fatalf("write dnsmasq log: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			DNSMasqLogPath: dnsmasqLog,
			NetworkLogPath: networkLog,
			Session:        session,
			Interval:       5 * time.Millisecond,
			FromStart:      true,
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(networkLog)
		if err == nil && len(raw) > 0 {
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			result, err := netlog.Scan(strings.NewReader(string(raw)))
			if err != nil {
				t.Fatalf("Scan() error = %v", err)
			}
			if len(result.Events) != 1 || result.Events[0].Domain != "telemetry.example.com" {
				t.Fatalf("network log = %+v", result.Events)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the translated event")
}

func TestRunRequiresADNSMasqLogPath(t *testing.T) {
	if err := Run(context.Background(), Options{NetworkLogPath: filepath.Join(t.TempDir(), "network.log")}); err == nil {
		t.Fatal("Run() accepted an empty dnsmasq log path")
	}
}
