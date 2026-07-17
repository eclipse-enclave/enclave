// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestBackgroundBackendRequestIncludesInteractiveTerminalEnv(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")

	r := &Runtime{
		profile: model.Profile{Name: "codex"},
		project: model.Project{Hash: "abc123abc123", Dir: "/work/project"},
		build:   model.BuildOptions{ImageName: "image"},
	}
	req := r.backendRequest(&ExecutionContext{ContainerName: "managed"}, true, true)

	if !req.Detached || !req.Session.Background {
		t.Fatalf("background request flags not set: Detached=%v Background=%v", req.Detached, req.Session.Background)
	}
	if got := backendEnvValue(req.Env, "TERM"); got != "xterm-256color" {
		t.Fatalf("TERM = %q, want xterm-256color", got)
	}
	if got := backendEnvValue(req.Env, "COLORTERM"); got != "truecolor" {
		t.Fatalf("COLORTERM = %q, want truecolor", got)
	}
}

func backendEnvValue(env []backend.EnvVar, key string) string {
	for _, entry := range env {
		if entry.Name == key {
			return entry.Value
		}
	}
	return ""
}
