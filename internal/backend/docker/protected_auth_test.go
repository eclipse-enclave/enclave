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
	"strings"
	"testing"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/util"
)

func TestProtectedAdmissionChecksBothEnginesWithoutChangingBinary(t *testing.T) {
	for _, tc := range []struct {
		name, dockerHolder, podmanHolder, storeKey, approvedEngine, unreachable string
		wantRefuse                                                              bool
	}{
		{name: "absent engines"},
		{name: "legacy Docker", dockerHolder: "legacy", wantRefuse: true},
		{name: "legacy Podman", podmanHolder: "legacy", wantRefuse: true},
		{name: "protected sibling", dockerHolder: "holder", approvedEngine: "docker"},
		{name: "wrong engine claim", podmanHolder: "holder", approvedEngine: "docker", wantRefuse: true},
		{name: "different suffix", dockerHolder: "legacy", storeKey: "other"},
		{name: "old missing key", podmanHolder: "legacy-unlabeled-key", wantRefuse: true},
		{name: "unreachable Docker", unreachable: "docker", wantRefuse: true},
		{name: "unreachable Podman", unreachable: "podman", wantRefuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			for _, engine := range []string{"docker", "podman"} {
				holder := tc.dockerHolder
				if engine == "podman" {
					holder = tc.podmanHolder
				}
				if holder == "" && tc.unreachable != engine {
					continue
				}
				key := tc.storeKey
				if key == "" && holder != "legacy-unlabeled-key" {
					key = "default"
				}
				labels := map[string]string{model.LabelAgent: "claude", model.LabelHash: "project", model.LabelConfigKey: key}
				if holder == "holder" {
					labels[model.LabelModeHolder] = holder
				}
				info := dockercmd.InspectResponse{ID: engine + "-cid", Name: "/" + engine + "-session",
					Config: &dockercmd.ContainerConfig{Labels: labels}, State: &dockercmd.ContainerState{Running: true, Status: "running"}}
				data, err := json.Marshal(info)
				if err != nil {
					t.Fatal(err)
				}
				script := "#!/bin/sh\nif [ \"$1\" = ps ]; then echo " + engine + "-cid; else printf '%s\\n' " + util.ShellQuote(string(data)) + "; fi\n"
				if tc.unreachable == engine {
					script = "#!/bin/sh\necho 'engine unreachable' >&2\nexit 1\n"
				}
				if err := os.WriteFile(filepath.Join(bin, engine), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			before := dockercmd.Binary()
			approved := map[string]string{"holder": tc.approvedEngine}
			err := New(Options{}).CheckProtectedAuthAdmission(context.Background(), backend.StoreKey{Owner: "claude", ProjectHash: "project"}, approved)
			if (err != nil) != tc.wantRefuse {
				t.Fatalf("admission error=%v want refusal=%t", err, tc.wantRefuse)
			}
			if tc.unreachable != "" && !strings.Contains(err.Error(), tc.unreachable) {
				t.Fatalf("unreachable engine not named: %v", err)
			}
			if err != nil && tc.unreachable == "" && !strings.Contains(err.Error(), "session") {
				t.Fatalf("holder not named: %v", err)
			}
			if dockercmd.Binary() != before {
				t.Fatal("admission changed the global engine used by the session")
			}
		})
	}
}
