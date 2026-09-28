// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/docker"
	"enclave/internal/model"
)

// withGatewayCLIStub installs a container CLI whose gateway never logs the
// ready marker but always reports a running container, and records every
// invocation in the returned log file. Tests override what `run` prints and
// exits with through STUB_RUN_OUTPUT and STUB_RUN_EXIT, and what inspect
// reports through STUB_INSPECT_JSON.
func withGatewayCLIStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	stub := filepath.Join(dir, "docker")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$STUB_CALL_LOG"
case "$1" in
run) printf '%s\n' "${STUB_RUN_OUTPUT-gw123}"; exit "${STUB_RUN_EXIT:-0}" ;;
container) printf '%s\n' "$STUB_INSPECT_JSON" ;;
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
	t.Setenv("STUB_INSPECT_JSON", stubInspectJSON(t, "running", true, nil))
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

	_, err := startGatewayContainer(ctx, &docker.ContainerConfig{Image: "gateway"}, &docker.HostConfig{}, "session-gateway")
	if err == nil {
		t.Fatal("expected an error when the context ends before the gateway is ready")
	}
	calls := stubCalls(t, logPath)
	if len(calls) == 0 || !strings.HasPrefix(calls[0], "run ") {
		t.Fatalf("expected the gateway to be started first, got %v", calls)
	}
	if !hasCall(calls, "rm --force --volumes gw123") {
		t.Fatalf("expected the unready gateway to be removed by ID, got %v", calls)
	}
}

// sessionGatewayConfig is the container config of the session whose gateway
// the start tests create, carrying the labels ownership checks compare.
func sessionGatewayConfig() *docker.ContainerConfig {
	return &docker.ContainerConfig{Image: "gateway", Labels: map[string]string{
		model.GatewayLabelManaged:     "true",
		model.GatewayLabelContainer:   "session",
		model.GatewayLabelProjectHash: "hash",
	}}
}

// stubInspectJSON renders the inspect view of a gateway container named
// session-gateway with ID gw123 in the given state and with the given labels.
func stubInspectJSON(t *testing.T, status string, running bool, labels map[string]string) string {
	t.Helper()
	data, err := json.Marshal(docker.InspectResponse{
		ID:     "gw123",
		Name:   "/session-gateway",
		Config: &docker.ContainerConfig{Labels: labels},
		State:  &docker.ContainerState{Status: status, Running: running},
	})
	if err != nil {
		t.Fatalf("marshal inspect stub: %v", err)
	}
	return string(data)
}

func assertNoCallWithPrefix(t *testing.T, calls []string, prefix string, why string) {
	t.Helper()
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			t.Fatalf("%s, got %v", why, calls)
		}
	}
}

func TestStartGatewayContainerRemovesSidecarWhenStartFails(t *testing.T) {
	logPath := withGatewayCLIStub(t)
	t.Setenv("STUB_INSPECT_JSON", stubInspectJSON(t, "created", false, sessionGatewayConfig().Labels))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := startGatewayContainer(ctx, sessionGatewayConfig(), &docker.HostConfig{}, "session-gateway")
	if err == nil || !strings.Contains(err.Error(), "failed to start gateway container") {
		t.Fatalf("expected a start failure, got %v", err)
	}
	calls := stubCalls(t, logPath)
	if !hasCall(calls, "rm --force --volumes gw123") {
		t.Fatalf("expected the created but unstarted gateway to be removed by its inspected ID, got %v", calls)
	}
	assertNoCallWithPrefix(t, calls, "logs ", "did not expect a readiness wait after a failed start")
}

func TestStartGatewayContainerRemovesSidecarByIDTheEnginePrinted(t *testing.T) {
	logPath := withGatewayCLIStub(t)
	printedID := strings.Repeat("ef", 32)
	t.Setenv("STUB_RUN_OUTPUT", printedID)
	t.Setenv("STUB_RUN_EXIT", "127")

	_, err := startGatewayContainer(context.Background(), sessionGatewayConfig(), &docker.HostConfig{}, "session-gateway")
	if err == nil || !strings.Contains(err.Error(), "failed to start gateway container") {
		t.Fatalf("expected a start failure, got %v", err)
	}
	calls := stubCalls(t, logPath)
	if !hasCall(calls, "rm --force --volumes "+printedID) {
		t.Fatalf("expected the gateway to be removed by the ID the engine printed, got %v", calls)
	}
	assertNoCallWithPrefix(t, calls, "container inspect", "did not expect a by-name lookup when the engine reported the ID")
}

func TestStartGatewayContainerLeavesRunningSidecarWhenStartFailsWithoutID(t *testing.T) {
	logPath := withGatewayCLIStub(t)
	t.Setenv("STUB_RUN_OUTPUT", "")
	t.Setenv("STUB_RUN_EXIT", "127")
	t.Setenv("STUB_INSPECT_JSON", stubInspectJSON(t, "running", true, sessionGatewayConfig().Labels))

	_, err := startGatewayContainer(context.Background(), sessionGatewayConfig(), &docker.HostConfig{}, "session-gateway")
	if err == nil {
		t.Fatal("expected a start failure")
	}
	calls := stubCalls(t, logPath)
	assertNoCallWithPrefix(t, calls, "rm ", "expected a running same-name gateway of a concurrent start to survive")
}

func TestStartGatewayContainerLeavesForeignSidecarWhenStartFailsWithoutID(t *testing.T) {
	logPath := withGatewayCLIStub(t)
	t.Setenv("STUB_RUN_OUTPUT", "")
	t.Setenv("STUB_RUN_EXIT", "127")
	t.Setenv("STUB_INSPECT_JSON", stubInspectJSON(t, "exited", false, map[string]string{
		model.GatewayLabelManaged:     "true",
		model.GatewayLabelContainer:   "other-session",
		model.GatewayLabelProjectHash: "hash",
	}))

	_, err := startGatewayContainer(context.Background(), sessionGatewayConfig(), &docker.HostConfig{}, "session-gateway")
	if err == nil {
		t.Fatal("expected a start failure")
	}
	calls := stubCalls(t, logPath)
	assertNoCallWithPrefix(t, calls, "rm ", "expected another session's gateway holding the name to survive")
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
