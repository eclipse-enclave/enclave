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

// The appender reuses one buffer and one encoder across events, so a missing
// reset would concatenate them into a single unreadable line.
func TestAppenderWritesOneLinePerEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	appender, err := NewAppender(path)
	if err != nil {
		t.Fatalf("NewAppender() error = %v", err)
	}
	for _, domain := range []string{"a.example", "b.example", "c.example"} {
		appender.Append(Event{Type: TypeTCP, Verdict: VerdictPass, Domain: domain})
	}
	if err := appender.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	result, err := Scan(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Events) != 3 || result.Skipped != 0 {
		t.Fatalf("got %d events and %d skipped, want 3 and 0: %q", len(result.Events), result.Skipped, string(raw))
	}
	for i, domain := range []string{"a.example", "b.example", "c.example"} {
		if result.Events[i].Domain != domain {
			t.Fatalf("event %d = %q, want %q", i, result.Events[i].Domain, domain)
		}
		// Append stamps the timestamp the writer left empty; without one the
		// reader would reject the line.
		if _, ok := result.Events[i].Time(); !ok {
			t.Fatalf("event %d has no usable timestamp: %+v", i, result.Events[i])
		}
	}
}

func TestAppenderWithoutAPathDiscards(t *testing.T) {
	appender, err := NewAppender("  ")
	if err != nil {
		t.Fatalf("NewAppender() error = %v", err)
	}
	appender.Append(Event{Type: TypeTCP, Verdict: VerdictPass, Domain: "a.example"})
	if err := appender.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
