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
	cfg := StartConfig{ContainerName: "enclave-demo-claude", NetworkLogPath: path}

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
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	// Grow the log past the cap without writing the bytes: only its size decides
	// whether it rotates.
	if err := os.Truncate(path, netlog.MaxLogBytes+1); err != nil {
		t.Fatalf("grow log: %v", err)
	}

	cfg := StartConfig{ContainerName: "enclave-demo-claude", NetworkLogPath: path}
	if err := prepareNetworkLog(cfg); err != nil {
		t.Fatalf("prepareNetworkLog() error = %v", err)
	}

	if events := readEvents(t, path); len(events) != 1 || !events[0].IsSessionMarker() {
		t.Fatalf("live log = %+v, want only the new session marker", events)
	}
	rotated, err := os.Stat(netlog.RotatedPath(path))
	if err != nil {
		t.Fatalf("stat rotated log: %v", err)
	}
	if rotated.Size() != netlog.MaxLogBytes+1 {
		t.Fatalf("rotated log is %d bytes, want the previous generation whole", rotated.Size())
	}
}

func TestPrepareNetworkLogKeepsHistoryBelowTheCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	if err := os.WriteFile(path, []byte(`{"ts":"2026-08-13T11:00:00Z","type":"tcp","verdict":"pass","domain":"old.example"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	cfg := StartConfig{ContainerName: "enclave-demo-claude", NetworkLogPath: path}
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
