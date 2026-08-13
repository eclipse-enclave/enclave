// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/netlog"
)

func readEvents(t *testing.T, path string) []netlog.Event {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- test-owned temp path.
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	result, err := netlog.Scan(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("Scan(%s) error = %v", path, err)
	}
	return result.Events
}

func TestPrepareNetworkLogRecordsSessionMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "network.log")
	cfg := StartConfig{ContainerName: "enclave-demo-claude", NetworkLogPath: path, NetworkLogMaxSize: "32MB"}

	if err := prepareNetworkLog(cfg); err != nil {
		t.Fatalf("prepareNetworkLog() error = %v", err)
	}
	events := readEvents(t, path)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one session marker", events)
	}
	marker := events[0]
	if !marker.IsSessionMarker() || marker.Rule != netlog.RuleSessionStart {
		t.Fatalf("marker = %+v", marker)
	}
	if marker.Session != cfg.ContainerName {
		t.Fatalf("marker session = %q, want %q", marker.Session, cfg.ContainerName)
	}
	if marker.Verdict != netlog.VerdictInfo {
		t.Fatalf("marker verdict = %q, want %q", marker.Verdict, netlog.VerdictInfo)
	}
}

func TestPrepareNetworkLogRotatesOverTheCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	old := `{"ts":"2026-08-13T11:00:00Z","type":"tcp","verdict":"pass","domain":"old.example"}` + "\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(old, 40)), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	cfg := StartConfig{ContainerName: "enclave-demo-claude", NetworkLogPath: path, NetworkLogMaxSize: "1KB"}
	if err := prepareNetworkLog(cfg); err != nil {
		t.Fatalf("prepareNetworkLog() error = %v", err)
	}

	if events := readEvents(t, path); len(events) != 1 || !events[0].IsSessionMarker() {
		t.Fatalf("live log = %+v, want only the new session marker", events)
	}
	if events := readEvents(t, netlog.RotatedPath(path)); len(events) != 40 {
		t.Fatalf("rotated log has %d events, want the previous 40", len(events))
	}
}

func TestPrepareNetworkLogKeepsHistoryBelowTheCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	if err := os.WriteFile(path, []byte(`{"ts":"2026-08-13T11:00:00Z","type":"tcp","verdict":"pass","domain":"old.example"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	cfg := StartConfig{ContainerName: "enclave-demo-claude", NetworkLogPath: path, NetworkLogMaxSize: "32MB"}
	if err := prepareNetworkLog(cfg); err != nil {
		t.Fatalf("prepareNetworkLog() error = %v", err)
	}
	if events := readEvents(t, path); len(events) != 2 {
		t.Fatalf("events = %+v, want the previous event plus the marker", events)
	}
	if _, err := os.Stat(netlog.RotatedPath(path)); !os.IsNotExist(err) {
		t.Fatal("rotated a log that was below the cap")
	}
}

func TestPrepareNetworkLogRejectsAMalformedCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	err := prepareNetworkLog(StartConfig{ContainerName: "enclave-demo-claude", NetworkLogPath: path, NetworkLogMaxSize: "32 gigs"})
	if err == nil {
		t.Fatal("prepareNetworkLog() accepted a malformed size")
	}
}
