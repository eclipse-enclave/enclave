// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/docker"
)

// withGatewayCLIStub installs a container CLI whose gateway never logs the
// ready marker but always reports a running container, and records every
// invocation in the returned log file.
func withGatewayCLIStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	stub := filepath.Join(dir, "docker")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$STUB_CALL_LOG"
case "$1" in
run) echo gw123 ;;
container) printf '%s\n' '{"Id":"gw123","Name":"/session-gateway","State":{"Status":"running","Running":true,"Error":""}}' ;;
esac
exit 0
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil { // #nosec G306 -- test stub must be executable.
		t.Fatalf("write cli stub: %v", err)
	}
	previous := docker.Binary()
	docker.SetBinary(stub)
	t.Cleanup(func() { docker.SetBinary(previous) })
	t.Setenv("STUB_CALL_LOG", logPath)
	return logPath
}

func stubCalls(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read stub call log: %v", err)
	}
	var calls []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			calls = append(calls, line)
		}
	}
	return calls
}

func hasCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func TestStartGatewayContainerRemovesSidecarWhenInterruptedBeforeReady(t *testing.T) {
	logPath := withGatewayCLIStub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := startGatewayContainer(ctx, &docker.ContainerConfig{Image: "gateway"}, &docker.HostConfig{}, "session-gateway")
	if err == nil {
		t.Fatal("expected an error when the context ends before the gateway is ready")
	}
	calls := stubCalls(t, logPath)
	if len(calls) == 0 || !strings.HasPrefix(calls[0], "run ") {
		t.Fatalf("expected the gateway to be started first, got %v", calls)
	}
	if !hasCall(calls, "rm --force --volumes session-gateway") {
		t.Fatalf("expected the unready gateway to be removed, got %v", calls)
	}
}

func TestStartGatewayContainerRemovesSidecarWhenStartFails(t *testing.T) {
	logPath := withGatewayCLIStub(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := startGatewayContainer(ctx, &docker.ContainerConfig{Image: "gateway"}, &docker.HostConfig{}, "session-gateway")
	if err == nil || !strings.Contains(err.Error(), "failed to start gateway container") {
		t.Fatalf("expected a start failure, got %v", err)
	}
	calls := stubCalls(t, logPath)
	if !hasCall(calls, "rm --force --volumes session-gateway") {
		t.Fatalf("expected a possibly created gateway to be removed, got %v", calls)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "logs ") {
			t.Fatalf("did not expect a readiness wait after a failed start, got %v", calls)
		}
	}
}

func TestWaitForGatewayReadyStopsOnCancelledContext(t *testing.T) {
	withGatewayCLIStub(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitForGatewayReady(ctx, "session-gateway", time.Now())
	if err == nil || !strings.Contains(err.Error(), "interrupted while waiting for gateway readiness") {
		t.Fatalf("expected an interruption error, got %v", err)
	}
}

func TestGatewayUserNSKeepsHostIDsUnderPodman(t *testing.T) {
	previous := docker.Binary()
	t.Cleanup(func() { docker.SetBinary(previous) })

	docker.SetBinary("docker")
	if got := gatewayUserNS(); got != "" {
		t.Fatalf("docker gateway must not set --userns, got %q", got)
	}
	if got := gatewayUser(); got != "" {
		t.Fatalf("docker gateway must keep the image user, got %q", got)
	}
	docker.SetBinary("podman")
	if got := gatewayUserNS(); got != "keep-id" {
		t.Fatalf("podman gateway must run with --userns keep-id, got %q", got)
	}
	if got := gatewayUser(); got != "0:0" {
		t.Fatalf("podman gateway must run as root inside keep-id, got %q", got)
	}
}
