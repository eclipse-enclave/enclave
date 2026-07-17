// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestResolveExecTargetReturnsDefaultForegroundContainer(t *testing.T) {
	r := &Runtime{
		project: model.Project{Name: "demo", Hash: "abc123"},
		profile: model.Profile{Name: "claude"},
		backend: &fakeBackend{sessions: []backend.Session{
			{Ref: backend.SessionRef{Name: "enclave-claude-abc123"}, Tool: "claude"},
		}},
	}

	got, err := r.resolveExecTarget()
	if err != nil {
		t.Fatalf("resolveExecTarget() returned error: %v", err)
	}
	if want := "enclave-claude-abc123"; got != want {
		t.Fatalf("resolveExecTarget() = %q, want %q", got, want)
	}
}

func TestResolveExecTargetPrefersDefaultForegroundContainer(t *testing.T) {
	r := &Runtime{
		project: model.Project{Name: "demo", Hash: "abc123"},
		profile: model.Profile{Name: "claude"},
		backend: &fakeBackend{sessions: []backend.Session{
			{Ref: backend.SessionRef{Name: "enclave-claude-abc123"}, Tool: "claude"},
			{Ref: backend.SessionRef{Name: "enclave-claude-abc123-2"}, Tool: "claude"},
		}},
	}

	got, err := r.resolveExecTarget()
	if err != nil {
		t.Fatalf("resolveExecTarget() returned error: %v", err)
	}
	if want := "enclave-claude-abc123"; got != want {
		t.Fatalf("resolveExecTarget() = %q, want %q", got, want)
	}
}

func TestResolveExecTargetPrefersCurrentWorktreeContainer(t *testing.T) {
	r := &Runtime{
		project: model.Project{Name: "demo", Hash: "abc123", Dir: "/tmp/repo-feature"},
		profile: model.Profile{Name: "claude"},
		backend: &fakeBackend{sessions: []backend.Session{
			{Ref: backend.SessionRef{Name: "enclave-claude-abc123"}, Tool: "claude", Worktree: "/tmp/repo"},
			{Ref: backend.SessionRef{Name: "enclave-claude-abc123-feature"}, Tool: "claude", Worktree: "/tmp/repo-feature", Name: "feature"},
		}},
	}

	got, err := r.resolveExecTarget()
	if err != nil {
		t.Fatalf("resolveExecTarget() returned error: %v", err)
	}
	if want := "enclave-claude-abc123-feature"; got != want {
		t.Fatalf("resolveExecTarget() = %q, want %q", got, want)
	}
}

func TestResolveExecTargetErrorsOnMultipleNamedSessions(t *testing.T) {
	r := &Runtime{
		project: model.Project{Name: "demo", Hash: "abc123"},
		profile: model.Profile{Name: "claude"},
		backend: &fakeBackend{sessions: []backend.Session{
			{Ref: backend.SessionRef{Name: "enclave-claude-abc123-2"}, Tool: "claude"},
			{Ref: backend.SessionRef{Name: "enclave-claude-abc123-3"}, Tool: "claude"},
		}},
	}

	_, err := r.resolveExecTarget()
	if err == nil {
		t.Fatal("resolveExecTarget() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "multiple sessions running") {
		t.Fatalf("resolveExecTarget() error = %q, want multiple sessions error", err)
	}
}

func TestResolveExecTargetSkipsGatewaySidecars(t *testing.T) {
	r := &Runtime{
		project: model.Project{Name: "demo", Hash: "abc123"},
		profile: model.Profile{Name: "claude"},
		backend: &fakeBackend{sessions: []backend.Session{
			{Ref: backend.SessionRef{Name: "enclave-claude-abc123-2"}, Tool: "claude"},
			{Ref: backend.SessionRef{Name: "enclave-claude-abc123-2-gateway"}, Tool: "claude"},
		}},
	}

	got, err := r.resolveExecTarget()
	if err != nil {
		t.Fatalf("resolveExecTarget() returned error: %v", err)
	}
	if want := "enclave-claude-abc123-2"; got != want {
		t.Fatalf("resolveExecTarget() = %q, want %q", got, want)
	}
}
