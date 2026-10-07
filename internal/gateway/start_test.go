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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/docker"
	"enclave/internal/model"
)

// The CLI stub records calls and simulates retained logs after a fast exit.
// STUB_* variables control run, inspect, and log responses.
func withGatewayCLIStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	stub := filepath.Join(dir, "docker")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$STUB_CALL_LOG"
case "$1" in
image) printf '{}\n' ;;
run)
    touch "$STUB_CALL_LOG.created"
    case " $* " in *" --rm "*) touch "$STUB_CALL_LOG.removed" ;; esac
    printf '%s\n' "${STUB_RUN_OUTPUT-gw123}"
    exit "${STUB_RUN_EXIT:-0}"
    ;;
container)
    if [ "${STUB_MISSING_BEFORE_RUN:-0}" = 1 ] && [ ! -f "$STUB_CALL_LOG.created" ]; then
        echo 'Error: No such container' >&2
        exit 1
    fi
    printf '%s\n' "$STUB_INSPECT_JSON"
    ;;
logs)
    if [ -f "$STUB_CALL_LOG.removed" ]; then
        echo 'Error: No such container' >&2
        exit 1
    fi
    if [ "$2" = --tail ]; then
        printf '%s\n' "${STUB_FINAL_LOGS:-}"
    else
        printf '%s\n' "${STUB_READY_LOGS:-}"
    fi
    exit "${STUB_LOG_EXIT:-0}"
    ;;
stop) exit "${STUB_STOP_EXIT:-0}" ;;
rm) touch "$STUB_CALL_LOG.removed" ;;
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

	_, err := startGatewayContainer(ctx, &docker.ContainerConfig{Image: "gateway"}, &docker.HostConfig{}, "session-gateway", nil)
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

func sessionGatewayConfig() *docker.ContainerConfig {
	return &docker.ContainerConfig{Image: "gateway", Labels: map[string]string{
		model.GatewayLabelManaged:     "true",
		model.GatewayLabelContainer:   "session",
		model.GatewayLabelProjectHash: "hash",
	}}
}

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

	_, err := startGatewayContainer(ctx, sessionGatewayConfig(), &docker.HostConfig{}, "session-gateway", nil)
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

	_, err := startGatewayContainer(context.Background(), sessionGatewayConfig(), &docker.HostConfig{}, "session-gateway", nil)
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

	_, err := startGatewayContainer(context.Background(), sessionGatewayConfig(), &docker.HostConfig{}, "session-gateway", nil)
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

	_, err := startGatewayContainer(context.Background(), sessionGatewayConfig(), &docker.HostConfig{}, "session-gateway", nil)
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

	err := waitForGatewayReady(ctx, "session-gateway", time.Now(), nil)
	if err == nil || !strings.Contains(err.Error(), "interrupted while waiting for gateway readiness") {
		t.Fatalf("expected an interruption error, got %v", err)
	}
}

func TestStartRetainsImmediateExitDiagnosticsUntilCleanup(t *testing.T) {
	logPath := withGatewayCLIStub(t)
	t.Setenv("STUB_MISSING_BEFORE_RUN", "1")
	t.Setenv("STUB_INSPECT_JSON", stubInspectJSON(t, "exited", false, sessionGatewayConfig().Labels))
	const diagnostic = "dnsmasq: cannot read /tmp/enclave-gateway-init.test/dnsmasq.conf: Permission denied"
	t.Setenv("STUB_FINAL_LOGS", "[enclave-gateway] dnsmasq config validation failed\n"+diagnostic)

	_, err := Start(context.Background(), StartConfig{
		Profile: model.Profile{Name: "claude"}, ContainerName: "session",
		NetworkName: "network", ProjectHash: "hash", Home: t.TempDir(), NoRebuild: true,
	})
	if err == nil || !strings.Contains(err.Error(), diagnostic) || !strings.Contains(err.Error(), "gateway exited during startup") {
		t.Fatalf("expected the original failure and final dnsmasq output, got %v", err)
	}
	calls := stubCalls(t, logPath)
	for _, call := range calls {
		if strings.HasPrefix(call, "run ") && strings.Contains(" "+call+" ", " --rm ") {
			t.Fatalf("startup must retain the container for diagnostics, got %s", call)
		}
	}
	if !hasCall(calls, "container inspect --format {{json .}} gw123") {
		t.Fatalf("readiness must inspect the immutable ID, got %v", calls)
	}
	if len(calls) < 2 || calls[len(calls)-2] != "logs --tail 40 gw123" || calls[len(calls)-1] != "rm --force --volumes gw123" {
		t.Fatalf("expected final logs before removal by ID, got %v", calls)
	}
}

func TestStartGatewayContainerReadyDoesNotRemoveOrReportLogs(t *testing.T) {
	logPath := withGatewayCLIStub(t)
	t.Setenv("STUB_READY_LOGS", "[enclave-gateway] Gateway ready")
	id, err := startGatewayContainer(context.Background(), sessionGatewayConfig(), &docker.HostConfig{}, "session-gateway")
	if err != nil || id != "gw123" {
		t.Fatalf("start = %q, %v", id, err)
	}
	calls := stubCalls(t, logPath)
	assertNoCallWithPrefix(t, calls, "rm ", "ready gateway must survive")
	assertNoCallWithPrefix(t, calls, "logs --tail", "successful starts need no error output")
}

func TestGatewayStartupErrorPreservesExpiredDeadlineAndBoundsOutput(t *testing.T) {
	withGatewayCLIStub(t)
	t.Setenv("STUB_FINAL_LOGS", strings.Repeat("x", 20*1024)+"\nfinal diagnostic")
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	err := gatewayStartupError(ctx, "gw123", context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "final diagnostic") {
		t.Fatalf("expected original deadline error and diagnostics, got %v", err)
	}
	if len(err.Error()) > 17*1024 || !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("unbounded or unmarked output: %d bytes", len(err.Error()))
	}
}

func TestGatewayStartupErrorSkipsLogsAfterUserCancellation(t *testing.T) {
	for _, cancelContext := range []bool{false, true} {
		logPath := withGatewayCLIStub(t)
		ctx, cancel := context.WithCancel(context.Background())
		cause := context.Canceled
		if cancelContext {
			cancel()
			cause = errors.New("engine command interrupted")
		}
		err := gatewayStartupError(ctx, "gw123", cause)
		cancel()
		if err != cause || len(stubCalls(t, logPath)) != 0 {
			t.Fatalf("cancellation must return immediately without reading logs: %v", err)
		}
	}
}

func TestGatewayStartupErrorLogFailurePreservesCause(t *testing.T) {
	withGatewayCLIStub(t)
	t.Setenv("STUB_LOG_EXIT", "1")
	cause := errors.New("gateway failed")
	if err := gatewayStartupError(context.Background(), "gw123", cause); err != cause {
		t.Fatalf("log failure hid original error: %v", err)
	}
}

func TestStopContainerRemovesRetainedGatewayByID(t *testing.T) {
	for _, stopExit := range []string{"0", "1"} {
		t.Run("stop exit "+stopExit, func(t *testing.T) {
			logPath := withGatewayCLIStub(t)
			t.Setenv("STUB_STOP_EXIT", stopExit)
			StopContainer("gw123")
			calls := stubCalls(t, logPath)
			if len(calls) != 2 || calls[0] != "stop --time 3 gw123" || calls[1] != "rm --force --volumes gw123" {
				t.Fatalf("expected stop then removal of the same ID, got %v", calls)
			}
		})
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

func TestOrdinaryGatewayRetainsGenericReadiness(t *testing.T) {
	logPath := withGatewayCLIStub(t)
	script := "#!/bin/sh\ncase \"$1\" in\nrun) echo gateway-id ;;\nlogs) echo '[enclave-gateway] Gateway ready' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(logPath), "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := startGatewayContainer(context.Background(), &docker.ContainerConfig{Image: "gateway"}, &docker.HostConfig{}, "session-gateway", nil); err != nil {
		t.Fatal(err)
	}
}
