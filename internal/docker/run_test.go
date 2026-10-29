// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestContainerIDFromRunOutput(t *testing.T) {
	full := strings.Repeat("ab", 32)
	for _, tc := range []struct{ name, out, want string }{
		{name: "full id", out: full + "\n", want: full},
		{name: "empty", out: "", want: ""},
		{name: "short", out: "abc123\n", want: ""},
		{name: "not hex", out: strings.Repeat("zz", 32), want: ""},
		{name: "first line only", out: full + "\nmore\n", want: full},
	} {
		if got := containerIDFromRunOutput(tc.out); got != tc.want {
			t.Errorf("%s: containerIDFromRunOutput(%q) = %q, want %q", tc.name, tc.out, got, tc.want)
		}
	}
}

func TestRunDetachedReturnsPrintedIDWhenStartFails(t *testing.T) {
	full := strings.Repeat("cd", 32)
	installNetworkDockerStub(t, `
printf '%s\n' `+full+`
printf '%s\n' 'docker: Error response from daemon: failed to create task for container' >&2
exit 127
`)
	id, err := RunDetached(context.Background(), &ContainerConfig{Image: "gateway"}, &HostConfig{}, "session-gateway")
	if err == nil {
		t.Fatal("expected RunDetached to fail")
	}
	if id != full {
		t.Fatalf("RunDetached() id = %q, want the printed ID %q", id, full)
	}
}

func TestDetachedInteractiveRunModeKeepsTTYAndStdin(t *testing.T) {
	args := buildRunArgs(&ContainerConfig{Image: "example:test"}, nil, "session", runMode{
		Detach:      true,
		Interactive: true,
		TTY:         true,
	})
	for _, want := range []string{"--detach", "--interactive", "--tty"} {
		if !slices.Contains(args, want) {
			t.Fatalf("expected %s in args %v", want, args)
		}
	}
}

func TestRunInteractiveWithStartHookWaitsForRunningContainer(t *testing.T) {
	withRunStartHookStub(t)
	t.Setenv("STUB_RUN_TOUCH", "1")
	t.Setenv("STUB_RUN_SLEEP", "0.3")
	t.Setenv("STUB_RUN_EXIT", "0")

	startedID := ""
	createdID, err := RunInteractiveWithStartHook(context.Background(), &ContainerConfig{Image: "example:test"}, nil, "session", "", func(id string) {
		startedID = id
	})
	if err != nil {
		t.Fatalf("RunInteractiveWithStartHook returned error: %v", err)
	}
	if startedID != stubContainerID {
		t.Fatalf("start hook got ID %q, want %s", startedID, stubContainerID)
	}
	if createdID != stubContainerID {
		t.Fatalf("created ID = %q, want %s", createdID, stubContainerID)
	}
}

func TestRunInteractiveWithStartHookSkipsHookWhenRunFailsBeforeStart(t *testing.T) {
	withRunStartHookStub(t)
	t.Setenv("STUB_RUN_SLEEP", "0")
	t.Setenv("STUB_RUN_EXIT", "125")

	called := false
	createdID, err := RunInteractiveWithStartHook(context.Background(), &ContainerConfig{Image: "example:test"}, nil, "session", "", func(string) {
		called = true
	})
	if err == nil {
		t.Fatal("expected run failure, got nil")
	}
	if called {
		t.Fatal("did not expect start hook to be called")
	}
	if createdID != "" {
		t.Fatalf("created ID = %q, want none", createdID)
	}
}

// A run that loses a name conflict must not adopt the container holding the
// name: its ID comes from the run's own cidfile, never from the name.
func TestRunWithStartHookIgnoresContainerHoldingTheName(t *testing.T) {
	withRunStartHookStub(t)
	t.Setenv("STUB_RUN_SLEEP", "0.3")
	t.Setenv("STUB_RUN_EXIT", "125")
	if err := os.WriteFile(os.Getenv("STUB_RUNNING_MARKER"), nil, 0o600); err != nil {
		t.Fatalf("mark existing container running: %v", err)
	}

	called := false
	createdID, err := RunWithStartHook(context.Background(), &ContainerConfig{Image: "example:test"}, nil, "session", func(string) {
		called = true
	})
	if err == nil {
		t.Fatal("expected run failure, got nil")
	}
	if called || createdID != "" {
		t.Fatalf("adopted the existing container: hook called %v, created ID %q", called, createdID)
	}
}

func TestRunWithStartHookWaitsForRunningContainer(t *testing.T) {
	withRunStartHookStub(t)
	t.Setenv("STUB_RUN_TOUCH", "1")
	t.Setenv("STUB_RUN_SLEEP", "0.3")
	t.Setenv("STUB_RUN_EXIT", "0")

	called := false
	_, err := RunWithStartHook(context.Background(), &ContainerConfig{Image: "example:test"}, nil, "session", func(string) {
		called = true
	})
	if err != nil {
		t.Fatalf("RunWithStartHook returned error: %v", err)
	}
	if !called {
		t.Fatal("expected start hook to be called")
	}
}

func TestRunWithIOAndStartHookWaitsForRunningContainer(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(map[bool]string{false: "without TTY", true: "with TTY"}[tty], func(t *testing.T) {
			withRunStartHookStub(t)
			t.Setenv("STUB_RUN_TOUCH", "1")
			t.Setenv("STUB_RUN_SLEEP", "0.3")
			t.Setenv("STUB_RUN_EXIT", "0")

			called := false
			_, err := RunWithIOAndStartHook(context.Background(), &ContainerConfig{Image: "example:test"}, nil, "session", nil, io.Discard, io.Discard, tty, "", func(string) {
				called = true
			})
			if err != nil {
				t.Fatalf("RunWithIOAndStartHook returned error: %v", err)
			}
			if !called {
				t.Fatal("expected start hook to be called")
			}
		})
	}
}

// Embedders that wire their own terminal pass detach keys through, like the
// foreground run on the process terminal.
func TestRunWithIOAndStartHookSetsDetachKeysOnlyWithTTY(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(map[bool]string{false: "without TTY", true: "with TTY"}[tty], func(t *testing.T) {
			withRunStartHookStub(t)
			argsLog := filepath.Join(t.TempDir(), "args")
			t.Setenv("STUB_RUN_ARGS", argsLog)
			if _, err := RunWithIOAndStartHook(context.Background(), &ContainerConfig{Image: "example:test"}, nil, "session", strings.NewReader(""), io.Discard, io.Discard, tty, "ctrl-x", nil); err != nil {
				t.Fatalf("RunWithIOAndStartHook returned error: %v", err)
			}
			args, err := os.ReadFile(argsLog)
			if err != nil {
				t.Fatalf("read run args: %v", err)
			}
			got := strings.Contains(string(args), "--detach-keys ctrl-x")
			if got != tty {
				t.Fatalf("detach keys set = %v, want %v (args %s)", got, tty, args)
			}
		})
	}
}

var stubContainerID = strings.Repeat("ab", 32)

// withRunStartHookStub installs a CLI whose run creates the container (writes
// its --cidfile and marks it running) when STUB_RUN_TOUCH is 1. Inspect
// reports any container as running once the marker exists.
func withRunStartHookStub(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "docker")
	runningMarker := filepath.Join(dir, "running")
	script := `#!/bin/sh
case "$1" in
run)
	if [ -n "$STUB_RUN_ARGS" ]; then
		printf '%s\n' "$*" > "$STUB_RUN_ARGS"
	fi
	if [ "$STUB_RUN_TOUCH" = "1" ]; then
		while [ $# -gt 0 ]; do
			[ "$1" = --cidfile ] && printf '%s' "$STUB_CONTAINER_ID" > "$2"
			shift
		done
		touch "$STUB_RUNNING_MARKER"
	fi
	sleep "${STUB_RUN_SLEEP:-0}"
	exit "${STUB_RUN_EXIT:-0}"
	;;
container)
	if [ "$2" = "inspect" ] && [ -e "$STUB_RUNNING_MARKER" ]; then
		printf '{"Id":"%s","Name":"/session","State":{"Status":"running","Running":true,"Error":""}}\n' "$STUB_CONTAINER_ID"
		exit 0
	fi
	printf '%s\n' 'Error: No such container: session' >&2
	exit 1
	;;
esac
exit 0
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	orig := dockerBinary
	dockerBinary = stub
	t.Cleanup(func() { dockerBinary = orig })
	t.Setenv("STUB_RUNNING_MARKER", runningMarker)
	t.Setenv("STUB_CONTAINER_ID", stubContainerID)
}
