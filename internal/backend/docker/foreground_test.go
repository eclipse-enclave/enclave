// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"testing"
)

// outlivingStubScript answers `container inspect` with the state in
// STUB_STATE: running, exited, or missing.
const outlivingStubScript = `case "$1" in
container)
	case "$STUB_STATE" in
	running) printf '%s\n' '{"Id":"s","Name":"/s","State":{"Status":"running","Running":true}}' ;;
	exited) printf '%s\n' '{"Id":"s","Name":"/s","State":{"Status":"exited","Running":false}}' ;;
	*) echo "Error: No such container: s" >&2; exit 1 ;;
	esac
	;;
esac
exit 0
`

// A foreground session ends with its terminal: a container the detach keys
// left running would otherwise lose its gateway and network.
func TestRemoveOutlivingContainer(t *testing.T) {
	for _, tc := range []struct {
		state      string
		wantRemove bool
	}{
		{"running", true},
		{"exited", false},
		{"missing", false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			log := stubCLI(t, "docker", outlivingStubScript)
			t.Setenv("STUB_STATE", tc.state)
			removeOutlivingContainer(context.Background(), "enclave-pi-abc123abc123")
			removed := hasCallWithPrefix(stubCalls(t, log), "rm --force enclave-pi-abc123abc123")
			if removed != tc.wantRemove {
				t.Errorf("removed = %v, want %v (calls %v)", removed, tc.wantRemove, stubCalls(t, log))
			}
		})
	}
}
