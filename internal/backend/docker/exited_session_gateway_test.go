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
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

const exitedSession = "enclave-pi-abc123abc123-leak"

// exitedSessionGatewayStubScript lists one running gateway "gwid" for
// exitedSession. STUB_SESSION_STATUS sets the session state ("missing" for no
// container), STUB_SESSION_HASH its project hash, STUB_SESSION_BACKGROUND its
// background label.
const exitedSessionGatewayStubScript = `eval last=\${$#}
case "$1" in
ps)
	echo gwid
	;;
container)
	case "$last" in
	gwid)
		printf '%s\n' '{"Id":"gwid","Name":"/@SESSION@-gateway","Config":{"Labels":{"MANAGED_LABEL":"true","CONTAINER_LABEL":"@SESSION@","PROJECT_LABEL":"abc123abc123"}},"State":{"Status":"running","Running":true}}'
		;;
	@SESSION@)
		if [ "$STUB_SESSION_STATUS" = "missing" ]; then
			echo "Error: No such container: $last" >&2
			exit 1
		fi
		running=false
		if [ "$STUB_SESSION_STATUS" = "running" ]; then
			running=true
		fi
		printf '%s\n' '{"Id":"sid","Name":"/@SESSION@","Config":{"Labels":{"HASH_LABEL":"'"$STUB_SESSION_HASH"'","BACKGROUND_LABEL":"'"$STUB_SESSION_BACKGROUND"'"}},"State":{"Status":"'"$STUB_SESSION_STATUS"'","Running":'"$running"'}}'
		;;
	esac
	;;
esac
exit 0
`

func stubExitedSessionGateway(t *testing.T, sessionStatus string, sessionHash string) string {
	t.Helper()
	t.Setenv("STUB_SESSION_STATUS", sessionStatus)
	t.Setenv("STUB_SESSION_HASH", sessionHash)
	t.Setenv("STUB_SESSION_BACKGROUND", "true")
	script := strings.NewReplacer(
		"MANAGED_LABEL", model.GatewayLabelManaged,
		"CONTAINER_LABEL", model.GatewayLabelContainer,
		"PROJECT_LABEL", model.GatewayLabelProjectHash,
		"HASH_LABEL", model.LabelHash,
		"BACKGROUND_LABEL", model.LabelBackground,
		"@SESSION@", exitedSession,
	).Replace(exitedSessionGatewayStubScript)
	return stubCLI(t, "docker", script)
}

func TestStopExitedSessionGatewaysStopsGatewayByID(t *testing.T) {
	for _, status := range []string{"exited", "dead", "stopped"} {
		t.Run(status, func(t *testing.T) {
			logPath := stubExitedSessionGateway(t, status, "abc123abc123")

			names, err := StopExitedSessionGateways(context.Background(), "enclave-pi-abc123abc123-", false)
			if err != nil {
				t.Fatalf("StopExitedSessionGateways: %v", err)
			}
			if !slices.Equal(names, []string{exitedSession + model.GatewayContainerSuffix}) {
				t.Fatalf("unexpected gateways %v", names)
			}
			if calls := stubCalls(t, logPath); !hasCallWithPrefix(calls, "stop --time 3 gwid") {
				t.Fatalf("expected the gateway to be stopped by ID, got %v", calls)
			}
		})
	}
}

func TestStopExitedSessionGatewaysLeavesGatewayRunning(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		hash       string
		foreground bool
		prefix     string
		dryRun     bool
		want       int
	}{
		{name: "running session", status: "running", hash: "abc123abc123"},
		{name: "starting session", status: "created", hash: "abc123abc123"},
		{name: "missing session", status: "missing", hash: "abc123abc123"},
		{name: "foreign session", status: "exited", hash: "other"},
		{name: "foreground session", status: "exited", hash: "abc123abc123", foreground: true},
		{name: "other owner", status: "exited", hash: "abc123abc123", prefix: "enclave-pi-other-"},
		{name: "dry run", status: "exited", hash: "abc123abc123", dryRun: true, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := stubExitedSessionGateway(t, tc.status, tc.hash)
			if tc.foreground {
				t.Setenv("STUB_SESSION_BACKGROUND", "")
			}

			names, err := StopExitedSessionGateways(context.Background(), tc.prefix, tc.dryRun)
			if err != nil {
				t.Fatalf("StopExitedSessionGateways: %v", err)
			}
			if len(names) != tc.want {
				t.Fatalf("expected %d gateways, got %v", tc.want, names)
			}
			if calls := stubCalls(t, logPath); hasCallWithPrefix(calls, "stop ") {
				t.Fatalf("unexpected gateway stop, got %v", calls)
			}
		})
	}
}

func TestAttachStopsGatewayOfExitedSession(t *testing.T) {
	logPath := stubExitedSessionGateway(t, "exited", "abc123abc123")

	if err := New(Options{}).Attach(context.Background(), backend.SessionRef{Name: exitedSession}, backend.AttachIO{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	calls := stubCalls(t, logPath)
	if !hasCallWithPrefix(calls, "ps --no-trunc --format {{.ID}} --filter label="+model.GatewayLabelContainer+"="+exitedSession+" ") {
		t.Fatalf("expected the listing to be limited to the session's gateway, got %v", calls)
	}
	if !hasCallWithPrefix(calls, "stop --time 3 gwid") {
		t.Fatalf("expected the gateway to be stopped by ID, got %v", calls)
	}
}
