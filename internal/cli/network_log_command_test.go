// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"strings"
	"testing"

	"enclave/internal/config"
)

func TestParseNetworkLogFlags(t *testing.T) {
	res, err := Parse([]string{
		"network", "log", "--follow", "--json", "--since", "10m",
		"--verdict", "deny", "--domain", "*.github.com", "--type", "http",
		"--session", "enclave-demo-claude", "--tool", "claude",
	}, config.DefaultOptions())
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "network-log" {
		t.Fatalf("action = %q, want network-log", res.Action)
	}
	view := res.NetworkLogView
	if !view.Follow || !view.JSON {
		t.Fatalf("view = %+v", view)
	}
	if view.Since != "10m" || view.Verdict != "deny" || view.Domain != "*.github.com" || view.Type != "http" {
		t.Fatalf("filters = %+v", view)
	}
	if view.Session != "enclave-demo-claude" {
		t.Fatalf("session = %q", view.Session)
	}
	if res.Options.AllRunning {
		t.Fatal("AllRunning should stay false without --all-running")
	}
}

func TestParseNetworkLogAllRunning(t *testing.T) {
	res, err := Parse([]string{"network", "log", "--all-running"}, config.DefaultOptions())
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !res.Options.AllRunning {
		t.Fatal("expected --all-running to set AllRunning")
	}
}

func TestParseNetworkLogRejectsConflicts(t *testing.T) {
	cases := map[string][]string{
		"follow with summary":       {"network", "log", "--follow", "--summary"},
		"since session all-running": {"network", "log", "--since", "session", "--all-running"},
		"session with all-running":  {"network", "log", "--session", "x", "--all-running"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(args, config.DefaultOptions()); err == nil {
				t.Fatalf("Parse(%v) accepted a conflicting combination", args)
			} else if strings.TrimSpace(err.Error()) == "" {
				t.Fatal("expected the error to name the conflict")
			}
		})
	}
}
