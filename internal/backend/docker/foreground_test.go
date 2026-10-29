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
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
)

// outlivingStubScript answers `container inspect` with the state in
// STUB_STATE (running, exited, or missing), or missing once the file
// STUB_GONE exists. It fails `stop` when STUB_STOP_FAILS is set, and fails
// `rm` like a racing --rm when STUB_RM_IN_PROGRESS is set, creating STUB_GONE
// unless STUB_RM_STUCK is set.
const outlivingStubScript = `case "$1" in
container)
	[ -n "$STUB_GONE" ] && [ -f "$STUB_GONE" ] && STUB_STATE=missing
	case "$STUB_STATE" in
	running) printf '%s\n' '{"Id":"cid","Name":"/s","State":{"Status":"running","Running":true}}' ;;
	exited) printf '%s\n' '{"Id":"cid","Name":"/s","State":{"Status":"exited","Running":false}}' ;;
	*) echo "Error: No such container: s" >&2; exit 1 ;;
	esac
	;;
stop)
	if [ -n "$STUB_STOP_FAILS" ]; then echo "Error: stop failed" >&2; exit 1; fi
	;;
rm)
	if [ -n "$STUB_RM_IN_PROGRESS" ]; then
		[ -z "$STUB_RM_STUCK" ] && : > "$STUB_GONE"
		echo "Error response from daemon: removal of container cid is already in progress" >&2
		exit 1
	fi
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
		{name: "no container created", state: "running"},
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
			if tc.wantInspect == "" && hasCallWithPrefix(calls, "container inspect") {
				t.Errorf("without an ID nothing may be inspected, calls %v", calls)
			}
			if tc.wantInspect != "" && !hasCallWithPrefix(calls, "container inspect --format {{json .}} "+tc.wantInspect) {
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

// The engine's --rm can already be removing the stopped container: that is not
// a failure, but the session must still be gone before its gateway.
func TestEndOutlivingSessionWaitsForRemovalInProgress(t *testing.T) {
	previous := endedContainerGoneTimeout
	endedContainerGoneTimeout = 300 * time.Millisecond
	t.Cleanup(func() { endedContainerGoneTimeout = previous })
	for _, tc := range []struct {
		name     string
		stuck    bool
		wantWarn bool
	}{
		{name: "removed by --rm"},
		{name: "never removed", stuck: true, wantWarn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := stubCLI(t, "docker", outlivingStubScript)
			gone := filepath.Join(t.TempDir(), "gone")
			t.Setenv("STUB_STATE", "running")
			t.Setenv("STUB_GONE", gone)
			t.Setenv("STUB_RM_IN_PROGRESS", "1")
			if tc.stuck {
				t.Setenv("STUB_RM_STUCK", "1")
			}
			var ended bool
			stderr := captureStderr(t, func() { ended = endOutlivingSession(context.Background(), "s", "cid") })
			if !ended {
				t.Fatalf("session not ended, calls %v", stubCalls(t, log))
			}
			if got := strings.Contains(stderr, "Failed to remove"); got != tc.wantWarn {
				t.Errorf("warned = %v, want %v (stderr %q)", got, tc.wantWarn, stderr)
			}
			calls := stubCalls(t, log)
			remove := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "rm --force cid") })
			if remove < 0 || !slices.ContainsFunc(calls[remove:], func(c string) bool { return strings.HasPrefix(c, "container inspect") }) {
				t.Errorf("expected a wait for removal after rm, calls %v", calls)
			}
		})
	}
}

// The caller's context never expires, so a stalled inspect must not outlast the
// removal timeout and hold up the gateway teardown.
func TestWaitContainerGoneBoundsStalledInspect(t *testing.T) {
	previous := endedContainerGoneTimeout
	endedContainerGoneTimeout = 50 * time.Millisecond
	t.Cleanup(func() { endedContainerGoneTimeout = previous })
	stubCLI(t, "docker", "exec sleep 2\n")

	start := time.Now()
	if waitContainerGone(context.Background(), "cid") {
		t.Fatal("stalled inspect reported the container gone")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waitContainerGone took %v, want about %v", elapsed, endedContainerGoneTimeout)
	}
}

// foregroundRunStub creates the container (writing STUB_CONTAINER_ID to the
// run's --cidfile) unless STUB_RUN_CONFLICT is set, in which case run fails
// like a name conflict. Inspect reports STUB_CONTAINER_ID running either way,
// standing in for whichever container holds the name.
const foregroundRunStub = `case "$1" in
run)
	if [ -n "$STUB_RUN_CONFLICT" ]; then
		echo "docker: Error response from daemon: Conflict. The container name is already in use." >&2
		exit 125
	fi
	while [ $# -gt 0 ]; do
		[ "$1" = --cidfile ] && printf '%s' "$STUB_CONTAINER_ID" > "$2"
		shift
	done
	sleep 0.3
	;;
container) printf '{"Id":"%s","Name":"/s","State":{"Status":"running","Running":true}}\n' "$STUB_CONTAINER_ID" ;;
esac
exit 0
`

var foregroundContainerID = strings.Repeat("c1", 32)

// The session must be ended before spec.cleanup removes the gateway whose
// network it uses, and only a caller with a start hook pays for polling.
func TestAttachForegroundEndsOutlivingSessionBeforeCleanup(t *testing.T) {
	const name = "enclave-pi-abc123abc123"
	for _, tc := range []struct {
		name string
		hook bool
	}{
		{name: "with start hook", hook: true},
		{name: "without start hook"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := stubCLI(t, "docker", foregroundRunStub)
			t.Setenv("STUB_CONTAINER_ID", foregroundContainerID)
			spec := foregroundSpec(t, name, log)
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
			for _, inspect := range inspects {
				if inspect != "container inspect --format {{json .}} "+foregroundContainerID {
					t.Errorf("inspected %q, want only the created container %s", inspect, foregroundContainerID)
				}
			}
			stop := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "stop "+foregroundContainerID) })
			remove := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "rm --force "+foregroundContainerID) })
			cleanup := slices.Index(calls, "cleanup")
			if stop < 0 || remove < stop || cleanup < remove {
				t.Errorf("want stop, remove, then cleanup; calls %v", calls)
			}
		})
	}
}

// A run that never created its container, such as one losing a name conflict,
// must leave the container holding the name alone and report its own failure.
func TestAttachForegroundSparesContainerHoldingTheName(t *testing.T) {
	const name = "enclave-pi-abc123abc123"
	log := stubCLI(t, "docker", foregroundRunStub)
	t.Setenv("STUB_CONTAINER_ID", foregroundContainerID)
	t.Setenv("STUB_RUN_CONFLICT", "1")
	started := false
	status, err := attachForeground(context.Background(), foregroundSpec(t, name, log), backend.AttachIO{
		In:        strings.NewReader(""),
		Out:       io.Discard,
		OnStarted: func() { started = true },
	})
	calls := stubCalls(t, log)
	if err == nil || status.Code == backend.ExitCodeInterrupted {
		t.Fatalf("attachForeground() = %v, %v; want the run failure", status, err)
	}
	if started {
		t.Error("OnStarted called for a container this run did not create")
	}
	if hasCallWithPrefix(calls, "stop") || hasCallWithPrefix(calls, "rm") {
		t.Errorf("the existing container must survive, calls %v", calls)
	}
	if !slices.Contains(calls, "cleanup") {
		t.Errorf("expected the gateway cleanup to run, calls %v", calls)
	}
}

// foregroundSpec returns a run spec whose cleanup appends "cleanup" to the
// stub's call log, so tests can order it against engine calls.
func foregroundSpec(t *testing.T, name string, log string) runSpec {
	t.Helper()
	return runSpec{
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
}

// A caller's detach keys win; otherwise the run uses the `enclave attach` key.
func TestForegroundDetachKeys(t *testing.T) {
	if got := foregroundDetachKeys(backend.AttachIO{}); got != model.DetachKeysDefault {
		t.Errorf("default = %q, want %q", got, model.DetachKeysDefault)
	}
	if got := foregroundDetachKeys(backend.AttachIO{DetachKeys: " ctrl-x "}); got != "ctrl-x" {
		t.Errorf("override = %q, want ctrl-x", got)
	}
}
