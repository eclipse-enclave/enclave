// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"strings"
	"testing"
	"time"

	"enclave/internal/gateway"
	"enclave/internal/model"
)

// staleGatewayStubScript answers `container inspect` for a session and its
// gateway. STUB_SESSION_EXISTS, STUB_GATEWAY_MISSING, STUB_GATEWAY_UNMANAGED
// and STUB_GATEWAY_CREATED select the scenario.
const staleGatewayStubScript = `case "$1" in
container)
	name="$5"
	case "$name" in
	*-gateway)
		if [ "$STUB_GATEWAY_MISSING" = "1" ]; then
			echo "Error: No such container: $name" >&2
			exit 1
		fi
		if [ "$STUB_GATEWAY_UNMANAGED" = "1" ]; then
			printf '%s\n' '{"Id":"gw","Name":"/gw","Created":"'"$STUB_GATEWAY_CREATED"'","Config":{"Labels":{}},"State":{"Status":"running","Running":true}}'
		else
			printf '%s\n' '{"Id":"gw","Name":"/gw","Created":"'"$STUB_GATEWAY_CREATED"'","Config":{"Labels":{"MANAGED_LABEL":"true","CONTAINER_LABEL":"enclave-pi-abc123abc123","PROJECT_LABEL":"abc123abc123"}},"State":{"Status":"running","Running":true}}'
		fi
		;;
	*)
		if [ "$STUB_SESSION_EXISTS" = "1" ]; then
			printf '%s\n' '{"Id":"s","Name":"/s","State":{"Status":"running","Running":true}}'
		else
			echo "Error: No such container: $name" >&2
			exit 1
		fi
		;;
	esac
	;;
esac
exit 0
`

func newStaleGatewayBackend(t *testing.T, created time.Time) (*Backend, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("STUB_GATEWAY_CREATED", created.UTC().Format(time.RFC3339Nano))
	script := strings.NewReplacer(
		"MANAGED_LABEL", model.GatewayLabelManaged,
		"CONTAINER_LABEL", model.GatewayLabelContainer,
		"PROJECT_LABEL", model.GatewayLabelProjectHash,
	).Replace(staleGatewayStubScript)
	logPath := stubCLI(t, "docker", script)
	return New(Options{Host: model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}}), logPath
}

func TestRemoveStaleGatewayRemovesOrphanedSidecarByID(t *testing.T) {
	b, logPath := newStaleGatewayBackend(t, time.Now().Add(-2*gateway.OrphanGracePeriod))

	if err := b.RemoveStaleGateway(context.Background(), "enclave-pi-abc123abc123", "abc123abc123"); err != nil {
		t.Fatalf("RemoveStaleGateway: %v", err)
	}
	calls := stubCalls(t, logPath)
	if !hasCallWithPrefix(calls, "rm --force --volumes gw") {
		t.Fatalf("expected the orphaned gateway to be removed by ID, got %v", calls)
	}
}

func TestRemoveStaleGatewayKeepsYoungSidecarWithHint(t *testing.T) {
	b, logPath := newStaleGatewayBackend(t, time.Now())

	out := captureStderr(t, func() {
		if err := b.RemoveStaleGateway(context.Background(), "enclave-pi-abc123abc123", "abc123abc123"); err != nil {
			t.Fatalf("RemoveStaleGateway: %v", err)
		}
	})
	if calls := stubCalls(t, logPath); hasCallWithPrefix(calls, "rm ") {
		t.Fatalf("a gateway of a possibly concurrent start must not be removed, got %v", calls)
	}
	if !strings.Contains(out, "enclave stop enclave-pi-abc123abc123") {
		t.Fatalf("expected a recovery hint, got %q", out)
	}
}

func TestRemoveStaleGatewayKeepsSidecarOfExistingSession(t *testing.T) {
	b, logPath := newStaleGatewayBackend(t, time.Now().Add(-2*gateway.OrphanGracePeriod))
	t.Setenv("STUB_SESSION_EXISTS", "1")

	out := captureStderr(t, func() {
		if err := b.RemoveStaleGateway(context.Background(), "enclave-pi-abc123abc123", "abc123abc123"); err != nil {
			t.Fatalf("RemoveStaleGateway: %v", err)
		}
	})
	if calls := stubCalls(t, logPath); hasCallWithPrefix(calls, "rm ") {
		t.Fatalf("a gateway with a live session must not be removed, got %v", calls)
	}
	if out != "" {
		t.Fatalf("a live session's gateway must not trigger a hint, got %q", out)
	}
}

func TestRemoveStaleGatewayIgnoresForeignAndMissingSidecars(t *testing.T) {
	for _, tc := range []struct {
		name        string
		env         string
		projectHash string
	}{
		{name: "unmanaged", env: "STUB_GATEWAY_UNMANAGED", projectHash: "abc123abc123"},
		{name: "missing", env: "STUB_GATEWAY_MISSING", projectHash: "abc123abc123"},
		{name: "other project", projectHash: "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, logPath := newStaleGatewayBackend(t, time.Now().Add(-2*gateway.OrphanGracePeriod))
			if tc.env != "" {
				t.Setenv(tc.env, "1")
			}

			if err := b.RemoveStaleGateway(context.Background(), "enclave-pi-abc123abc123", tc.projectHash); err != nil {
				t.Fatalf("RemoveStaleGateway: %v", err)
			}
			if calls := stubCalls(t, logPath); hasCallWithPrefix(calls, "rm ") {
				t.Fatalf("unexpected removal, got %v", calls)
			}
		})
	}
}
