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
	"time"

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
	case " $* " in
	*' status=running '*) [ "$STUB_GATEWAY_STATUS" = running ] || exit 0 ;;
	esac
	echo gwid
	;;
container)
	case "$last" in
	gwid)
		running=false
		if [ "$STUB_GATEWAY_STATUS" = running ]; then running=true; fi
		printf '%s\n' '{"Id":"gwid","Name":"/'"$STUB_GATEWAY_NAME"'","Config":{"Labels":{"MANAGED_LABEL":"'"$STUB_GATEWAY_MANAGED"'","CONTAINER_LABEL":"@SESSION@","PROJECT_LABEL":"'"$STUB_GATEWAY_HASH"'"}},"State":{"Status":"'"$STUB_GATEWAY_STATUS"'","Running":'"$running"',"FinishedAt":"'"$STUB_FINISHED_AT"'"}}'
		;;
	@SESSION@)
		if [ "$STUB_SESSION_STATUS" = "inspect-error" ]; then
			echo 'Error: permission denied' >&2
			exit 1
		fi
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
rm)
	if [ "$STUB_SESSION_STATUS" != missing ]; then
		echo 'Error: container gwid has dependent containers which must be removed before it' >&2
		exit 1
	fi
	;;
esac
exit 0
`

func stubExitedSessionGateway(t *testing.T, sessionStatus string, sessionHash string) string {
	t.Helper()
	t.Setenv("STUB_GATEWAY_STATUS", "running")
	t.Setenv("STUB_GATEWAY_NAME", exitedSession+model.GatewayContainerSuffix)
	t.Setenv("STUB_GATEWAY_HASH", "abc123abc123")
	t.Setenv("STUB_GATEWAY_MANAGED", "true")
	t.Setenv("STUB_FINISHED_AT", time.Now().Add(-2*time.Minute).UTC().Format(time.RFC3339Nano))
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
			calls := stubCalls(t, logPath)
			if !hasCallWithPrefix(calls, "stop --time 3 gwid") || hasCallWithPrefix(calls, "rm ") {
				t.Fatalf("expected stop-only while the dependent session exists, got %v", calls)
			}
		})
	}
}

func TestStopExitedSessionGatewaysKeepsStoppedGatewayWithDependentSession(t *testing.T) {
	for _, status := range []string{"exited", "dead", "stopped"} {
		t.Run(status, func(t *testing.T) {
			logPath := stubExitedSessionGateway(t, "exited", "abc123abc123")
			t.Setenv("STUB_GATEWAY_STATUS", status)
			_, err := StopExitedSessionGateways(context.Background(), "", false)
			if err != nil {
				t.Fatal(err)
			}
			calls := stubCalls(t, logPath)
			if hasCallWithPrefix(calls, "stop ") || hasCallWithPrefix(calls, "rm ") {
				t.Fatalf("stopped gateway must wait for session removal: %v", calls)
			}
		})
	}
}

func TestStopExitedSessionGatewaysLeavesCreatedGateway(t *testing.T) {
	logPath := stubExitedSessionGateway(t, "exited", "abc123abc123")
	t.Setenv("STUB_GATEWAY_STATUS", "created")
	_, err := StopExitedSessionGateways(context.Background(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	calls := stubCalls(t, logPath)
	if hasCallWithPrefix(calls, "stop ") || hasCallWithPrefix(calls, "rm ") {
		t.Fatalf("must not tear down a gateway still being created: %v", calls)
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
