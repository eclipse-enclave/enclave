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

func TestNextSessionNameStartsAtOneWhenDefaultContainerExists(t *testing.T) {
	r := &Runtime{
		profile: model.Profile{Name: "claude"},
		project: model.Project{Hash: "abc123"},
		backend: &fakeBackend{sessions: []backend.Session{
			{Ref: backend.SessionRef{Name: "enclave-claude-abc123"}, Tool: "claude"},
		}},
	}

	got := r.nextSessionName()

	if got != "1" {
		t.Fatalf("nextSessionName() = %q, want %q", got, "1")
	}
}

func TestNextSessionNameIncrementsExistingNumericSuffix(t *testing.T) {
	r := &Runtime{
		profile: model.Profile{Name: "claude"},
		project: model.Project{Hash: "abc123"},
		backend: &fakeBackend{sessions: []backend.Session{
			{Ref: backend.SessionRef{Name: "enclave-claude-abc123"}, Tool: "claude"},
			{Ref: backend.SessionRef{Name: "enclave-claude-abc123-1"}, Tool: "claude", Name: "1"},
		}},
	}

	got := r.nextSessionName()

	if got != "2" {
		t.Fatalf("nextSessionName() = %q, want %q", got, "2")
	}
}

func TestNextSessionNameUsesNamePrefixForLegacyContainers(t *testing.T) {
	var gotFilter backend.SessionFilter
	r := &Runtime{
		profile: model.Profile{Name: "claude"},
		project: model.Project{Hash: "abc123abc123"},
		backend: &fakeBackend{listFn: func(filter backend.SessionFilter) ([]backend.Session, error) {
			gotFilter = filter
			return []backend.Session{
				{Ref: backend.SessionRef{Name: "enclave-claude-abc123abc123-7"}},
			}, nil
		}},
	}

	got := r.nextSessionName()

	if got != "8" {
		t.Fatalf("nextSessionName() = %q, want 8", got)
	}
	if gotFilter.NamePrefix != "enclave-claude-abc123abc123-" {
		t.Fatalf("NamePrefix = %q, want legacy name prefix", gotFilter.NamePrefix)
	}
	if gotFilter.Tool != "" || gotFilter.ProjectHash != "" {
		t.Fatalf("legacy scan should not require labels, got filter %+v", gotFilter)
	}
}
