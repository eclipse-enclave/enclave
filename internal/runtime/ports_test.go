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

// theiaProfile is a minimal profile whose post_start launches Theia, matching
// the gating in applyTheiaAPIPort.
func theiaProfile() model.Profile {
	return model.Profile{Name: "theia", PostStart: &model.PostStartActions{OpenIDE: "theia"}}
}

func TestApplyTheiaAPIPortInjectsBarePort(t *testing.T) {
	r := &Runtime{run: model.RunOptions{TheiaAPIPort: "3333"}, profile: theiaProfile()}
	r.applyTheiaAPIPort()
	if !portListHas(r.run.Ports, "3333") {
		t.Errorf("expected 3333 injected, got %v", r.run.Ports)
	}
}

func TestApplyTheiaAPIPortNoopWhenUnset(t *testing.T) {
	r := &Runtime{run: model.RunOptions{}, profile: theiaProfile()}
	r.applyTheiaAPIPort()
	if len(r.run.Ports) != 0 {
		t.Errorf("expected no ports when unset, got %v", r.run.Ports)
	}
}

func TestApplyTheiaAPIPortSkipsUserMappedPort(t *testing.T) {
	r := &Runtime{run: model.RunOptions{Ports: []string{"0.0.0.0:3333:3333"}, TheiaAPIPort: "3333"}, profile: theiaProfile()}
	r.applyTheiaAPIPort()
	if len(r.run.Ports) != 1 {
		t.Errorf("expected no duplicate for user-mapped 3333, got %v", r.run.Ports)
	}
}

func TestApplyTheiaAPIPortSkipsNonTheiaTool(t *testing.T) {
	r := &Runtime{run: model.RunOptions{TheiaAPIPort: "3333"}, profile: model.Profile{Name: "claude"}}
	r.applyTheiaAPIPort()
	if len(r.run.Ports) != 0 {
		t.Errorf("expected no port published for non-Theia tool, got %v", r.run.Ports)
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

func TestLogPublishedPortURLsUsesExplicitHostPort(t *testing.T) {
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
		r.logPublishedPortURLs()
	})
	if !strings.Contains(output, "http://localhost:8080") {
		t.Fatalf("expected URL to use host port 8080, got %q", output)
	}
	if strings.Contains(output, "http://localhost:3000") {
		t.Fatalf("expected URL not to use container port 3000, got %q", output)
	}
}
