// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"slices"
	"testing"
	"time"

	"enclave/internal/model"
)

func TestStopExitedSessionGatewaysReclaimsStoppedOrphans(t *testing.T) {
	for _, status := range []string{"exited", "dead", "stopped"} {
		for _, dryRun := range []bool{false, true} {
			name := status
			if dryRun {
				name += "/dry-run"
			}
			t.Run(name, func(t *testing.T) {
				logPath := stubExitedSessionGateway(t, "missing", "abc123abc123")
				t.Setenv("STUB_GATEWAY_STATUS", status)
				names, err := StopExitedSessionGateways(context.Background(), "enclave-pi-abc123abc123-", dryRun)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(names, []string{exitedSession + model.GatewayContainerSuffix}) {
					t.Fatalf("unexpected orphan list %v", names)
				}
				calls := stubCalls(t, logPath)
				if hasCallWithPrefix(calls, "stop ") || hasCallWithPrefix(calls, "rm ") == dryRun {
					t.Fatalf("unexpected lifecycle calls: %v", calls)
				}
				if !dryRun && !slices.Contains(calls, "rm --volumes gwid") {
					t.Fatalf("orphan must be removed without force and by immutable ID: %v", calls)
				}
			})
		}
	}
}

func TestStopExitedSessionGatewaysProtectsOtherGateways(t *testing.T) {
	for _, tc := range []struct {
		name      string
		env       map[string]string
		prefix    string
		wantError bool
	}{
		{name: "recent exit", env: map[string]string{"STUB_FINISHED_AT": time.Now().UTC().Format(time.RFC3339Nano)}},
		{name: "future exit", env: map[string]string{"STUB_FINISHED_AT": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}},
		{name: "missing timestamp", env: map[string]string{"STUB_FINISHED_AT": ""}},
		{name: "invalid timestamp", env: map[string]string{"STUB_FINISHED_AT": "invalid"}},
		{name: "zero timestamp", env: map[string]string{"STUB_FINISHED_AT": "0001-01-01T00:00:00Z"}},
		{name: "running orphan", env: map[string]string{"STUB_GATEWAY_STATUS": "running"}},
		{name: "created orphan", env: map[string]string{"STUB_GATEWAY_STATUS": "created"}},
		{name: "paused orphan", env: map[string]string{"STUB_GATEWAY_STATUS": "paused"}},
		{name: "existing session", env: map[string]string{"STUB_SESSION_STATUS": "exited"}},
		{name: "running session", env: map[string]string{"STUB_SESSION_STATUS": "running"}},
		{name: "foreign session", env: map[string]string{"STUB_SESSION_STATUS": "exited", "STUB_SESSION_HASH": "other"}},
		{name: "session inspection failed", env: map[string]string{"STUB_SESSION_STATUS": "inspect-error"}, wantError: true},
		{name: "unmanaged gateway", env: map[string]string{"STUB_GATEWAY_MANAGED": "false"}},
		{name: "foreign hash", env: map[string]string{"STUB_GATEWAY_HASH": "other"}},
		{name: "missing hash", env: map[string]string{"STUB_GATEWAY_HASH": ""}},
		{name: "foreign name", env: map[string]string{"STUB_GATEWAY_NAME": "foreign-gateway"}},
		{name: "different project scope", prefix: "enclave-pi-other-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := stubExitedSessionGateway(t, "missing", "abc123abc123")
			t.Setenv("STUB_GATEWAY_STATUS", "exited")
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			names, err := StopExitedSessionGateways(context.Background(), tc.prefix, false)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(names) != 0 {
				t.Fatalf("protected gateway must not be listed: %v", names)
			}
			calls := stubCalls(t, logPath)
			if hasCallWithPrefix(calls, "stop ") || hasCallWithPrefix(calls, "rm ") {
				t.Fatalf("must leave gateway untouched: %v", calls)
			}
		})
	}
}
