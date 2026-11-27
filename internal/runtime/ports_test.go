// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/util"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdout = writer

	defer func() {
		os.Stdout = original
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(data)
}

func portListHas(ports []string, want string) bool {
	for _, p := range ports {
		if p == want {
			return true
		}
	}
	return false
}

type portHintHandler struct {
	noopRuntimeHandler
	ports []string
}

func (h portHintHandler) PortHints(model.RunContext) []string { return h.ports }

func portHintRuntime(t *testing.T, ports ...string) *Runtime {
	t.Helper()
	return &Runtime{
		host:    model.Host{Home: t.TempDir()},
		profile: model.Profile{Name: "pi"},
		handler: portHintHandler{ports: ports},
	}
}

// heldLoopbackPort listens on a free loopback port until the test ends.
func heldLoopbackPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func portHintLockIsFree(t *testing.T, r *Runtime, port string) bool {
	t.Helper()
	release, ok, err := util.TryAcquireFileLock(config.HostPortHintLockPath(r.host.Home, port))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		release()
	}
	return ok
}

func TestApplyPortHintsMapsFreePortUntilStarted(t *testing.T) {
	port := freeLoopbackPort(t)
	r := portHintRuntime(t, port)
	releaseStartLock, err := r.acquireSessionStartLock()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseStartLock()

	var runCtx model.RunContext
	out := captureStdout(t, func() { r.applyPortHints(&runCtx) })

	if !portListHas(r.run.Ports, port) || !portListHas(runCtx.Run.Ports, port) {
		t.Fatalf("expected the free port %s to be mapped, got %v and %v", port, r.run.Ports, runCtx.Run.Ports)
	}
	if len(runCtx.SkippedPortHints) != 0 {
		t.Fatalf("expected no skipped hint, got %v", runCtx.SkippedPortHints)
	}
	if !strings.Contains(out, "Auto-mapped port "+port+" for Pi") {
		t.Fatalf("expected the auto-mapping to be announced, got %q", out)
	}
	if portHintLockIsFree(t, r, port) {
		t.Fatal("expected the port hint lock to be held until the session has started")
	}
	releaseStartLock()
	if !portHintLockIsFree(t, r, port) {
		t.Fatal("expected the session start to release the port hint lock")
	}
}

func TestApplyPortHintsSkipsPortInUse(t *testing.T) {
	tests := []struct {
		name  string
		state model.ProviderAuthState
		warns bool
	}{
		{name: "no credential", warns: true},
		{name: "env credential", state: model.ProviderAuthState{HasEnvCredential: true}},
		{name: "session", state: model.ProviderAuthState{HasSession: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := heldLoopbackPort(t)
			r := portHintRuntime(t, port)
			runCtx := model.RunContext{AuthState: model.AuthState{
				Providers: map[string]model.ProviderAuthState{"anthropic": tt.state, "openai-codex": {}},
			}}

			var stdout string
			stderr := captureStderr(t, func() {
				stdout = captureStdout(t, func() { r.applyPortHints(&runCtx) })
			})

			if len(r.run.Ports) != 0 || len(runCtx.Run.Ports) != 0 {
				t.Fatalf("expected the port in use to stay unmapped, got %v and %v", r.run.Ports, runCtx.Run.Ports)
			}
			if !portListHas(runCtx.SkippedPortHints, port) {
				t.Fatalf("expected %s to be recorded as skipped, got %v", port, runCtx.SkippedPortHints)
			}
			message := "Port " + port + " is in use on 127.0.0.1 and was not mapped for Pi"
			warned, informed := strings.Contains(stderr, message), strings.Contains(stdout, message)
			if warned != tt.warns || informed == tt.warns {
				t.Fatalf("expected warning=%v, got stderr %q and stdout %q", tt.warns, stderr, stdout)
			}
			if !portHintLockIsFree(t, r, port) {
				t.Fatal("expected a session that maps no port to release the port hint lock at once")
			}
		})
	}
}

func TestApplyPortHintsLocksOnlyTheMappedPort(t *testing.T) {
	free, held := freeLoopbackPort(t), heldLoopbackPort(t)
	r := portHintRuntime(t, held, free, held)
	releaseStartLock, err := r.acquireSessionStartLock()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseStartLock()

	runCtx := model.RunContext{AuthState: model.AuthState{
		Providers: map[string]model.ProviderAuthState{"openai-codex": {HasEnvCredential: true}},
	}}
	out := captureStdout(t, func() { r.applyPortHints(&runCtx) })

	if len(r.run.Ports) != 1 || r.run.Ports[0] != free {
		t.Fatalf("expected only the free port %s to be mapped, got %v", free, r.run.Ports)
	}
	if len(runCtx.SkippedPortHints) != 1 || runCtx.SkippedPortHints[0] != held {
		t.Fatalf("expected the port in use %s to be skipped once, got %v", held, runCtx.SkippedPortHints)
	}
	if count := strings.Count(out, "Port "+held+" is in use"); count != 1 {
		t.Fatalf("expected one message for the port in use, got %d in %q", count, out)
	}
	if portHintLockIsFree(t, r, free) {
		t.Fatal("expected the lock of the mapped port to be held until the session has started")
	}
	if !portHintLockIsFree(t, r, held) {
		t.Fatal("expected the lock of the port in use to be released at once")
	}
	releaseStartLock()
	if !portHintLockIsFree(t, r, free) {
		t.Fatal("expected the session start to release the lock of the mapped port")
	}
}

func TestApplyPortHintsLeavesUserMappingsAlone(t *testing.T) {
	port := heldLoopbackPort(t)
	for _, spec := range []string{port, port + ":3000", "3000:" + port} {
		r := portHintRuntime(t, port)
		r.run.Ports = []string{spec}
		var runCtx model.RunContext

		r.applyPortHints(&runCtx)

		if len(r.run.Ports) != 1 || r.run.Ports[0] != spec {
			t.Errorf("expected the user mapping %s to stay as it is, got %v", spec, r.run.Ports)
		}
		if len(runCtx.SkippedPortHints) != 0 {
			t.Errorf("expected no skipped hint beside the user mapping %s, got %v", spec, runCtx.SkippedPortHints)
		}
	}
}

func TestApplyProfilePortsInjectsPublishedPorts(t *testing.T) {
	r := &Runtime{
		profile: model.Profile{
			Name: "theia",
			Ports: []model.PortConfig{
				{Container: 3000, Publish: true, Label: "Theia IDE"},
				{Container: 9999, Publish: false, Label: "hidden"},
			},
		},
	}
	r.applyProfilePorts()
	if !portListHas(r.run.Ports, "3000") {
		t.Errorf("expected 3000 injected, got %v", r.run.Ports)
	}
	if portListHas(r.run.Ports, "9999") {
		t.Errorf("did not expect unpublished 9999, got %v", r.run.Ports)
	}
}

func TestApplyProfilePortsSkipsUserMappedPort(t *testing.T) {
	r := &Runtime{
		run: model.RunOptions{Ports: []string{"3000"}},
		profile: model.Profile{
			Ports: []model.PortConfig{{Container: 3000, Publish: true, Label: "x"}},
		},
	}
	r.applyProfilePorts()
	if len(r.run.Ports) != 1 {
		t.Errorf("expected no duplicate for user-mapped 3000, got %v", r.run.Ports)
	}
}

func TestApplyProfilePortsSkipsHostIPMappedPort(t *testing.T) {
	r := &Runtime{
		run: model.RunOptions{Ports: []string{"0.0.0.0:3000:3000"}},
		profile: model.Profile{
			Ports: []model.PortConfig{{Container: 3000, Publish: true, Label: "x"}},
		},
	}
	r.applyProfilePorts()
	if len(r.run.Ports) != 1 {
		t.Errorf("expected no duplicate for host-IP mapped 3000, got %v", r.run.Ports)
	}
}

func TestLogDeclaredPortAvailabilityUsesExplicitHostPort(t *testing.T) {
	r := &Runtime{
		run: model.RunOptions{Ports: []string{"8080:3000"}},
		profile: model.Profile{
			Ports: []model.PortConfig{{
				Container: 3000,
				Publish:   true,
				Label:     "Theia IDE",
				OpenURL:   "http://localhost:{host_port}",
			}},
		},
	}
	r.applyProfilePorts()

	output := captureStdout(t, func() {
		r.logDeclaredPortAvailability(nil)
	})
	if !strings.Contains(output, "http://localhost:8080") {
		t.Fatalf("expected URL to use host port 8080, got %q", output)
	}
	if strings.Contains(output, "http://localhost:3000") {
		t.Fatalf("expected URL not to use container port 3000, got %q", output)
	}
}

func TestAnnouncePublishedPortsPrintsExplicitForwarding(t *testing.T) {
	r := &Runtime{
		run:     model.RunOptions{Ports: []string{"8080:3000", "0.0.0.0:9000:9000"}},
		profile: model.Profile{Name: "claude"},
	}

	output := captureStdout(t, func() {
		r.announcePublishedPorts("enclave-test")
	})
	if !strings.Contains(output, "Port forwarding: 127.0.0.1:8080 -> 3000") {
		t.Fatalf("expected loopback-defaulted forwarding line, got %q", output)
	}
	if !strings.Contains(output, "Port forwarding: 0.0.0.0:9000 -> 9000") {
		t.Fatalf("expected explicit host-IP forwarding line, got %q", output)
	}
}

func TestAnnouncePublishedPortsResolvesAutoAssignedHostPort(t *testing.T) {
	r := &Runtime{
		run: model.RunOptions{Ports: []string{"127.0.0.1:0:3000"}},
		profile: model.Profile{
			Ports: []model.PortConfig{{
				Container: 3000,
				Publish:   true,
				Label:     "Theia IDE",
				OpenURL:   "http://localhost:{host_port}",
			}},
		},
		backend: &fakeBackend{inspectFn: func(ref backend.SessionRef) (*backend.Session, error) {
			if ref.Name != "enclave-test" {
				t.Errorf("expected inspect of enclave-test, got %q", ref.Name)
			}
			return &backend.Session{Ports: []backend.PortMapping{
				{HostIP: "127.0.0.1", HostPort: "49321", ContainerPort: "3000", Protocol: "tcp"},
			}}, nil
		}},
	}
	r.applyProfilePorts()

	output := captureStdout(t, func() {
		r.announcePublishedPorts("enclave-test")
	})
	if !strings.Contains(output, "Port forwarding: 127.0.0.1:49321 -> 3000") {
		t.Fatalf("expected completed forwarding line, got %q", output)
	}
	if !strings.Contains(output, "http://localhost:49321") {
		t.Fatalf("expected URL with the live host port, got %q", output)
	}
}

func TestAnnouncePublishedPortsCompletesForwardingWithoutOpenURL(t *testing.T) {
	r := &Runtime{
		run:     model.RunOptions{Ports: []string{"127.0.0.1:0:2000"}},
		profile: model.Profile{Name: "claude"},
		backend: &fakeBackend{inspectFn: func(backend.SessionRef) (*backend.Session, error) {
			return &backend.Session{Ports: []backend.PortMapping{
				{HostIP: "127.0.0.1", HostPort: "49500", ContainerPort: "2000", Protocol: "tcp"},
			}}, nil
		}},
	}

	output := captureStdout(t, func() {
		r.announcePublishedPorts("enclave-test")
	})
	if !strings.Contains(output, "Port forwarding: 127.0.0.1:49500 -> 2000") {
		t.Fatalf("expected completed forwarding line, got %q", output)
	}
}

func TestAnnouncePublishedPortsAutoPortWithoutBindingFallsBack(t *testing.T) {
	r := &Runtime{
		run: model.RunOptions{Ports: []string{"127.0.0.1:0:3000"}},
		profile: model.Profile{
			Ports: []model.PortConfig{{
				Container: 3000,
				Publish:   true,
				Label:     "Theia IDE",
				OpenURL:   "http://localhost:{host_port}",
			}},
		},
	}
	r.applyProfilePorts()

	output := captureStdout(t, func() {
		r.announcePublishedPorts("enclave-test")
	})
	if strings.Contains(output, "http://localhost:0") {
		t.Fatalf("expected no URL with the 0 sentinel, got %q", output)
	}
	if !strings.Contains(output, "Port forwarding: 127.0.0.1:<auto> -> 3000") {
		t.Fatalf("expected <auto> placeholder forwarding line, got %q", output)
	}
	if !strings.Contains(output, "enclave ps") {
		t.Fatalf("expected hint at enclave ps, got %q", output)
	}
}

func TestApplyFeaturePortsPublishesEnabledFeatureDeclarations(t *testing.T) {
	r := &Runtime{
		profile: model.Profile{Name: "claude"},
		features: []model.Extension{
			{Name: "diffity", Ports: []model.PortConfig{
				{Container: 5391, HostAllocation: model.HostAllocationAuto, Publish: true, Label: "Diffity"},
			}},
			{Name: "fixed", Ports: []model.PortConfig{
				{Container: 8080, Publish: true, Label: "Fixed"},
			}},
			{Name: "quiet", Ports: []model.PortConfig{
				{Container: 9999, Label: "Quiet"}, // publish omitted
			}},
		},
	}
	r.applyFeaturePorts()
	if !portListHas(r.run.Ports, "0:5391") {
		t.Errorf("expected auto-host spec 0:5391, got %v", r.run.Ports)
	}
	if !portListHas(r.run.Ports, "8080") {
		t.Errorf("expected fixed port 8080, got %v", r.run.Ports)
	}
	if portListHas(r.run.Ports, "9999") || portListHas(r.run.Ports, "0:9999") {
		t.Errorf("did not expect unpublished 9999, got %v", r.run.Ports)
	}
}

func TestApplyFeaturePortsUserMappingWins(t *testing.T) {
	r := &Runtime{
		run:     model.RunOptions{Ports: []string{"127.0.0.1:6000:5391"}},
		profile: model.Profile{Name: "claude"},
		features: []model.Extension{
			{Name: "diffity", Ports: []model.PortConfig{
				{Container: 5391, HostAllocation: model.HostAllocationAuto, Publish: true, Label: "Diffity"},
			}},
		},
	}
	r.applyFeaturePorts()
	if len(r.run.Ports) != 1 {
		t.Errorf("expected user mapping to win over feature declaration, got %v", r.run.Ports)
	}
}

func TestApplyFeaturePortsTwoAutoPortsDoNotCollide(t *testing.T) {
	r := &Runtime{
		profile: model.Profile{Name: "claude"},
		features: []model.Extension{
			{Name: "a", Ports: []model.PortConfig{
				{Container: 5391, HostAllocation: model.HostAllocationAuto, Publish: true, Label: "A"},
			}},
			{Name: "b", Ports: []model.PortConfig{
				{Container: 6006, HostAllocation: model.HostAllocationAuto, Publish: true, Label: "B"},
			}},
		},
	}
	r.applyFeaturePorts()
	if !portListHas(r.run.Ports, "0:5391") || !portListHas(r.run.Ports, "0:6006") {
		t.Errorf("expected both auto-host specs, got %v", r.run.Ports)
	}
}

func TestDeclaredPortConfigsCombinesProfileAndFeatures(t *testing.T) {
	r := &Runtime{
		profile: model.Profile{
			Name:  "theia",
			Ports: []model.PortConfig{{Container: 3000, Publish: true, Label: "Theia IDE"}},
		},
		features: []model.Extension{
			{Name: "diffity", Ports: []model.PortConfig{
				{Container: 5391, HostAllocation: model.HostAllocationAuto, Publish: true, Label: "Diffity"},
			}},
		},
	}
	got := r.declaredPortConfigs()
	if len(got) != 2 || got[0].Label != "Theia IDE" || got[1].Label != "Diffity" {
		t.Fatalf("declaredPortConfigs = %+v", got)
	}
}

func TestLogDeclaredPortAvailabilityAutoPortWithoutBackendFallsBack(t *testing.T) {
	r := &Runtime{
		profile: model.Profile{Name: "claude"},
		features: []model.Extension{
			{Name: "diffity", Ports: []model.PortConfig{{
				Container:      5391,
				HostAllocation: model.HostAllocationAuto,
				Publish:        true,
				Label:          "Diffity",
				OpenURL:        "http://localhost:{host_port}",
			}}},
		},
	}
	r.applyFeaturePorts()

	output := captureStdout(t, func() {
		r.logDeclaredPortAvailability(nil)
	})
	if strings.Contains(output, "http://localhost:0") {
		t.Fatalf("must not print the 0 sentinel as a URL, got %q", output)
	}
	if !strings.Contains(output, "auto-assigned") {
		t.Fatalf("expected auto-assigned fallback hint, got %q", output)
	}
}
