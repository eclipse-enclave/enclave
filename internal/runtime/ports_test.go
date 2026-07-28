// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"io"
	"os"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
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
