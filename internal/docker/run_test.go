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

	called := false
	err := RunInteractiveWithStartHook(context.Background(), &ContainerConfig{Image: "example:test"}, nil, "session", func() {
		called = true
	})
	if err != nil {
		t.Fatalf("RunInteractiveWithStartHook returned error: %v", err)
	}
	if !called {
		t.Fatal("expected start hook to be called")
	}
}

func TestRunInteractiveWithStartHookSkipsHookWhenRunFailsBeforeStart(t *testing.T) {
	withRunStartHookStub(t)
	t.Setenv("STUB_RUN_SLEEP", "0")
	t.Setenv("STUB_RUN_EXIT", "125")

	called := false
	err := RunInteractiveWithStartHook(context.Background(), &ContainerConfig{Image: "example:test"}, nil, "session", func() {
		called = true
	})
	if err == nil {
		t.Fatal("expected run failure, got nil")
	}
	if called {
		t.Fatal("did not expect start hook to be called")
	}
}

func TestRunWithStartHookWaitsForRunningContainer(t *testing.T) {
	withRunStartHookStub(t)
	t.Setenv("STUB_RUN_TOUCH", "1")
	t.Setenv("STUB_RUN_SLEEP", "0.3")
	t.Setenv("STUB_RUN_EXIT", "0")

	called := false
	err := RunWithStartHook(context.Background(), &ContainerConfig{Image: "example:test"}, nil, "session", func() {
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
			err := RunWithIOAndStartHook(context.Background(), &ContainerConfig{Image: "example:test"}, nil, "session", nil, io.Discard, io.Discard, tty, func() {
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

func withRunStartHookStub(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "docker")
	runningMarker := filepath.Join(dir, "running")
	script := `#!/bin/sh
case "$1" in
run)
	if [ "$STUB_RUN_TOUCH" = "1" ]; then
		touch "$STUB_RUNNING_MARKER"
	fi
	sleep "${STUB_RUN_SLEEP:-0}"
	exit "${STUB_RUN_EXIT:-0}"
	;;
container)
	if [ "$2" = "inspect" ] && [ -e "$STUB_RUNNING_MARKER" ]; then
		printf '%s\n' '{"Id":"abc","Name":"/session","State":{"Status":"running","Running":true,"Error":""}}'
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
}
