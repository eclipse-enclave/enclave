// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
	"enclave/internal/network"
)

func TestNetworkModeReadsPolicyFiles(t *testing.T) {
	const projectHash = "p1"
	for _, tc := range []struct {
		name        string
		global      string
		project     string
		projectHash string
		want        string
	}{
		{"no policy files", "", "", projectHash, model.NetworkModeRestricted},
		{"global sets the mode", model.NetworkModeUnrestricted, "", projectHash, model.NetworkModeUnrestricted},
		{"project overrides global", model.NetworkModeUnrestricted, model.NetworkModeRestricted, projectHash, model.NetworkModeRestricted},
		{"project alone sets the mode", "", model.NetworkModeUnrestricted, projectHash, model.NetworkModeUnrestricted},
		{"no project ignores the project file", "", model.NetworkModeUnrestricted, "", model.NetworkModeRestricted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.global != "" {
				savePolicyMode(t, network.GlobalPolicyPath(home), tc.global)
			}
			if tc.project != "" {
				savePolicyMode(t, network.ProjectPolicyPath(home, projectHash), tc.project)
			}
			got, err := NetworkMode(home, tc.projectHash)
			if err != nil {
				t.Fatalf("NetworkMode() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("NetworkMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNetworkModeReportsBrokenPolicyFile(t *testing.T) {
	home := t.TempDir()
	for _, path := range []string{network.GlobalPolicyPath(home), network.ProjectPolicyPath(home, "p1")} {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			writeBrokenPolicy(t, path)
			defer func() { _ = os.Remove(path) }()
			_, err := NetworkMode(home, "p1")
			if err == nil {
				t.Fatalf("NetworkMode() accepted a broken policy file %s", path)
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("NetworkMode() error = %v, want it to name %s", err, path)
			}
		})
	}
}

func savePolicyMode(t *testing.T, path string, mode string) {
	t.Helper()
	if err := network.SavePolicy(path, network.Policy{Mode: mode}); err != nil {
		t.Fatal(err)
	}
}

func writeBrokenPolicy(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
}
