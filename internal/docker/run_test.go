// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

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
