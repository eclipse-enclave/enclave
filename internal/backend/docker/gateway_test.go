// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/gateway/mitm"
	"enclave/internal/model"
)

func TestWriteSecretReleaseConfig(t *testing.T) {
	secrets := []backend.SecretRelease{
		{
			SecretID:    "api-key",
			Placeholder: "ENCLAVE_SECRET_abc",
			Value:       "real",
			HTTP: &backend.HTTPReleaseRule{
				Hosts:  []string{"api.example.com"},
				Header: "x-api-key",
			},
		},
	}
	path, err := writeSecretReleaseConfig(secrets)
	if err != nil {
		t.Fatalf("writeSecretReleaseConfig() error = %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	var payload []model.SecretReleaseEntry
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(payload))
	}
	if payload[0].Placeholder != "ENCLAVE_SECRET_abc" {
		t.Fatalf("placeholder = %q, want %q", payload[0].Placeholder, "ENCLAVE_SECRET_abc")
	}
	loaded, err := mitm.LoadRules(path)
	if err != nil {
		t.Fatalf("LoadRules(%q) error = %v", path, err)
	}
	expected := []model.SecretReleaseEntry{
		{
			SecretID:    "api-key",
			Placeholder: "ENCLAVE_SECRET_abc",
			Value:       "real",
			Hosts:       []string{"api.example.com"},
			Header:      "x-api-key",
		},
	}
	if !reflect.DeepEqual(loaded, expected) {
		t.Fatalf("LoadRules(%q) = %#v, want %#v", path, loaded, expected)
	}
}

func TestWorkspaceIDFromSessionPrefersRealWorktree(t *testing.T) {
	got := workspaceIDFromSession(backend.SessionMeta{
		Worktree:     "/workspace/project",
		RealWorktree: "/real/project",
	}, "/workspace/project")
	if got != "/real/project" {
		t.Fatalf("workspaceIDFromSession() = %q, want real worktree", got)
	}
}
