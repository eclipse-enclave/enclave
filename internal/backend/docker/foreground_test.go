// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
)

// outlivingStubScript answers `container inspect` with the state in
// STUB_STATE (running, exited, or missing) and fails `stop` when
// STUB_STOP_FAILS is set.
const outlivingStubScript = `case "$1" in
container)
	case "$STUB_STATE" in
	running) printf '%s\n' '{"Id":"cid","Name":"/s","State":{"Status":"running","Running":true}}' ;;
	exited) printf '%s\n' '{"Id":"cid","Name":"/s","State":{"Status":"exited","Running":false}}' ;;
	*) echo "Error: No such container: s" >&2; exit 1 ;;
	esac
	;;
stop)
	if [ -n "$STUB_STOP_FAILS" ]; then echo "Error: stop failed" >&2; exit 1; fi
	;;
esac
exit 0
`

// A foreground session ends with its terminal: a container the detach chord
// left running would otherwise lose its gateway and network.
func TestEndOutlivingSession(t *testing.T) {
	const name = "enclave-pi-abc123abc123"
	for _, tc := range []struct {
		name        string
		id          string
		state       string
		stopFails   bool
		wantInspect string
		wantEnded   bool
		wantStop    bool
		wantRemove  bool
	}{
		{name: "running", id: "cid", state: "running", wantInspect: "cid", wantEnded: true, wantStop: true, wantRemove: true},
		{name: "exited", id: "cid", state: "exited", wantInspect: "cid"},
		{name: "replaced under its name", id: "cid", state: "missing", wantInspect: "cid"},
		{name: "never seen running", state: "running", wantInspect: name, wantEnded: true, wantStop: true, wantRemove: true},
		{name: "stop fails", id: "cid", state: "running", stopFails: true, wantInspect: "cid", wantEnded: true, wantStop: true, wantRemove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := stubCLI(t, "docker", outlivingStubScript)
			t.Setenv("STUB_STATE", tc.state)
			if tc.stopFails {
				t.Setenv("STUB_STOP_FAILS", "1")
			}
			ended := endOutlivingSession(context.Background(), name, tc.id)
			calls := stubCalls(t, log)
			if ended != tc.wantEnded {
				t.Errorf("ended = %v, want %v (calls %v)", ended, tc.wantEnded, calls)
			}
			if !hasCallWithPrefix(calls, "container inspect --format {{json .}} "+tc.wantInspect) {
				t.Errorf("expected inspect of %s, calls %v", tc.wantInspect, calls)
			}
			if got := hasCallWithPrefix(calls, "stop cid"); got != tc.wantStop {
				t.Errorf("stopped by ID = %v, want %v (calls %v)", got, tc.wantStop, calls)
			}
			if got := hasCallWithPrefix(calls, "rm --force cid"); got != tc.wantRemove {
				t.Errorf("force-removed by ID = %v, want %v (calls %v)", got, tc.wantRemove, calls)
			}
			if hasCallWithPrefix(calls, "rm --force "+name) || hasCallWithPrefix(calls, "stop "+name) {
				t.Errorf("container must not be ended by name, calls %v", calls)
			}
		})
	}
}

// The session must be ended before spec.cleanup removes the gateway whose
// network it uses, and only a caller with a start hook pays for polling.
func TestAttachForegroundEndsOutlivingSessionBeforeCleanup(t *testing.T) {
	const name = "enclave-pi-abc123abc123"
	for _, tc := range []struct {
		name        string
		hook        bool
		wantInspect string
	}{
		{name: "with start hook", hook: true, wantInspect: "cid"},
		{name: "without start hook", wantInspect: name},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := stubCLI(t, "docker", `case "$1" in
run) sleep 0.3 ;;
container) printf '%s\n' '{"Id":"cid","Name":"/s","State":{"Status":"running","Running":true}}' ;;
esac
exit 0
`)
			spec := runSpec{
				config: &dockercmd.ContainerConfig{Image: "image"},
				name:   name,
				cleanup: func() {
					f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
					if err != nil {
						t.Errorf("open call log: %v", err)
						return
					}
					if _, err := f.WriteString("cleanup\n"); err != nil {
						t.Errorf("append cleanup marker: %v", err)
					}
					if err := f.Close(); err != nil {
						t.Errorf("close call log: %v", err)
					}
				},
			}
			started := 0
			attach := backend.AttachIO{In: strings.NewReader(""), Out: io.Discard}
			if tc.hook {
				attach.OnStarted = func() { started++ }
			}
			status, err := attachForeground(context.Background(), spec, attach)
			calls := stubCalls(t, log)
			var exitErr *backend.ExitError
			if status.Code != backend.ExitCodeInterrupted || !errors.As(err, &exitErr) || exitErr.Code != backend.ExitCodeInterrupted {
				t.Fatalf("attachForeground() = %v, %v; want status %d", status, err, backend.ExitCodeInterrupted)
			}
			if tc.hook && started != 1 {
				t.Errorf("OnStarted called %d times, want 1", started)
			}
			inspects := slices.DeleteFunc(slices.Clone(calls), func(c string) bool { return !strings.HasPrefix(c, "container inspect") })
			if !tc.hook && len(inspects) != 1 {
				t.Errorf("without a start hook expected a single inspect, got %v", inspects)
			}
			if last := inspects[len(inspects)-1]; last != "container inspect --format {{json .}} "+tc.wantInspect {
				t.Errorf("outliving check inspected %q, want %s", last, tc.wantInspect)
			}
			stop := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "stop cid") })
			remove := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "rm --force cid") })
			cleanup := slices.Index(calls, "cleanup")
			if stop < 0 || remove < stop || cleanup < remove {
				t.Errorf("want stop, remove, then cleanup; calls %v", calls)
			}
		})
	}
}
