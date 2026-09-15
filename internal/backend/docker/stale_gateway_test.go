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

	"enclave/internal/model"
)

// staleGatewayStubScript answers `container inspect` for a session and its
// gateway. STUB_SESSION_EXISTS, STUB_GATEWAY_MISSING and STUB_GATEWAY_UNMANAGED
// select the scenario.
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
			printf '%s\n' '{"Id":"gw","Name":"/gw","Config":{"Labels":{}},"State":{"Status":"running","Running":true}}'
		else
			printf '%s\n' '{"Id":"gw","Name":"/gw","Config":{"Labels":{"MANAGED_LABEL":"true"}},"State":{"Status":"running","Running":true}}'
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

func newStaleGatewayBackend(t *testing.T) (*Backend, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	script := strings.ReplaceAll(staleGatewayStubScript, "MANAGED_LABEL", model.GatewayLabelManaged)
	logPath := stubCLI(t, "docker", script)
	return New(Options{Host: model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}}), logPath
}

func TestRemoveStaleGatewayRemovesOrphanedSidecar(t *testing.T) {
	b, logPath := newStaleGatewayBackend(t)

	if err := b.RemoveStaleGateway(context.Background(), "enclave-pi-abc123abc123"); err != nil {
		t.Fatalf("RemoveStaleGateway: %v", err)
	}
	calls := stubCalls(t, logPath)
	if !hasCallWithPrefix(calls, "rm --force --volumes enclave-pi-abc123abc123-gateway") {
		t.Fatalf("expected the orphaned gateway to be removed, got %v", calls)
	}
}

func TestRemoveStaleGatewayKeepsSidecarOfExistingSession(t *testing.T) {
	b, logPath := newStaleGatewayBackend(t)
	t.Setenv("STUB_SESSION_EXISTS", "1")

	if err := b.RemoveStaleGateway(context.Background(), "enclave-pi-abc123abc123"); err != nil {
		t.Fatalf("RemoveStaleGateway: %v", err)
	}
	if calls := stubCalls(t, logPath); hasCallWithPrefix(calls, "rm ") {
		t.Fatalf("a gateway with a live session must not be removed, got %v", calls)
	}
}

func TestRemoveStaleGatewayIgnoresUnmanagedAndMissingSidecars(t *testing.T) {
	for _, env := range []string{"STUB_GATEWAY_UNMANAGED", "STUB_GATEWAY_MISSING"} {
		t.Run(env, func(t *testing.T) {
			b, logPath := newStaleGatewayBackend(t)
			t.Setenv(env, "1")

			if err := b.RemoveStaleGateway(context.Background(), "enclave-pi-abc123abc123"); err != nil {
				t.Fatalf("RemoveStaleGateway: %v", err)
			}
			if calls := stubCalls(t, logPath); hasCallWithPrefix(calls, "rm ") {
				t.Fatalf("unexpected removal, got %v", calls)
			}
		})
	}
}
