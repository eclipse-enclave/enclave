// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package docker

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A SIGTERM aimed at the enclave process while a session is attached must reach
// the engine child, which proxies it into the container. The test survives the
// signal the way the runtime does, through signal.NotifyContext.
func TestRunInteractiveForwardsProcessSignalsToChild(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "docker")
	started := filepath.Join(dir, "started")
	signalLog := filepath.Join(dir, "signals")
	script := `#!/bin/sh
case "$1" in
run)
	trap 'printf TERM >> "$STUB_SIGNAL_LOG"; exit 0' TERM
	touch "$STUB_STARTED_MARKER"
	while :; do sleep 0.05; done
	;;
esac
exit 0
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil { // #nosec G306 -- test stub must be executable.
		t.Fatalf("write docker stub: %v", err)
	}
	orig := dockerBinary
	dockerBinary = stub
	t.Cleanup(func() { dockerBinary = orig })
	t.Setenv("STUB_STARTED_MARKER", started)
	t.Setenv("STUB_SIGNAL_LOG", signalLog)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	t.Cleanup(stop)

	result := make(chan error, 1)
	go func() {
		result <- RunInteractiveWithStartHook(context.Background(), &ContainerConfig{Image: "example:test"}, nil, "session", nil)
	}()
	waitForFile(t, started)

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not exit after the forwarded SIGTERM")
	}
	if ctx.Err() == nil {
		t.Fatal("expected the signal to reach this process as well")
	}
	got, err := os.ReadFile(signalLog)
	if err != nil {
		t.Fatalf("child did not log the signal: %v", err)
	}
	if string(got) != "TERM" {
		t.Fatalf("child saw %q, want TERM", got)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not appear", path)
}
