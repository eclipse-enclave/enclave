// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/util"
)

func TestAstraProtectedSiblingWithPodmanDockerShim(t *testing.T) {
	bin := t.TempDir()
	info := dockercmd.InspectResponse{ID: "podman-cid", Name: "/protected-sibling",
		Config: &dockercmd.ContainerConfig{Labels: map[string]string{
			model.LabelAgent: "claude", model.LabelHash: "project", model.LabelConfigKey: "default", model.LabelModeHolder: "holder",
		}}, State: &dockercmd.ContainerState{Running: true, Status: "running"}}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in\n--version) echo 'podman version 4.3.1';;\nps) echo podman-cid;;\n*) printf '%s\\n' " + util.ShellQuote(string(data)) + ";;\nesac\n"
	for _, engine := range []string{"docker", "podman"} {
		if err := os.WriteFile(filepath.Join(bin, engine), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	if got := dockercmd.DetectCLIs(); len(got) != 1 || got[0] != "podman" {
		t.Fatalf("shim detection control: %v", got)
	}
	be := New(Options{Engine: "podman"})
	err = be.CheckProtectedAuthAdmission(context.Background(), backend.StoreKey{Owner: "claude", ProjectHash: "project"}, map[string]string{"holder": "podman"})
	if err != nil {
		t.Fatalf("known protected Podman sibling refused through docker shim: %v", err)
	}
}
