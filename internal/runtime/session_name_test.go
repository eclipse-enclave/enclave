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

func TestSessionDisplayNameKeepsUserSuppliedName(t *testing.T) {
	r := &Runtime{
		profile: model.Profile{Name: "claude"},
		project: model.Project{Hash: "abc123abc123"},
		run:     model.RunOptions{Background: true, SessionName: "My Task"},
	}

	containerName := r.containerName()
	if containerName != "enclave-claude-abc123abc123-my-task" {
		t.Fatalf("containerName() = %q", containerName)
	}
	if got := r.sessionDisplayName(containerName, true); got != "My Task" {
		t.Fatalf("sessionDisplayName() = %q, want the user-supplied name in the session label", got)
	}
}

func TestSessionDisplayNameEmptyForDefaultForegroundContainer(t *testing.T) {
	r := &Runtime{
		profile: model.Profile{Name: "claude"},
		project: model.Project{Hash: "abc123abc123"},
	}

	if got := r.sessionDisplayName(r.baseContainerName(), false); got != "" {
		t.Fatalf("sessionDisplayName() = %q, want no session label", got)
	}
}

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

func TestSessionStartLockNameCoordinatesSharedStartupState(t *testing.T) {
	r := Runtime{
		profile: model.Profile{Name: "claude"},
		project: model.Project{Hash: "abc123abc123"},
	}
	named := r
	named.run.SessionName = "task-a"
	sharedLock := named.sessionStartLockName()
	for _, tc := range []struct {
		name string
		run  model.RunOptions
	}{
		{name: "unnamed"},
		{name: "background", run: model.RunOptions{Background: true}},
		{name: "same name", run: model.RunOptions{SessionName: "Task A"}},
		{name: "different name shares gateway config", run: model.RunOptions{SessionName: "task-b"}},
		{name: "named background", run: model.RunOptions{Background: true, SessionName: "task-b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := r
			other.run = tc.run
			if got := other.sessionStartLockName(); got != sharedLock {
				t.Fatalf("start lock = %q, want the shared project/tool lock %q", got, sharedLock)
			}
		})
	}

	otherTool := r
	otherTool.profile.Name = "codex"
	if otherTool.sessionStartLockName() == sharedLock {
		t.Fatal("different tools must not share a start lock")
	}
	otherProject := r
	otherProject.project.Hash = "def456def456"
	if otherProject.sessionStartLockName() == sharedLock {
		t.Fatal("different projects must not share a start lock")
	}
}

func TestSessionStartLockNameCoversExplicitNumericCollision(t *testing.T) {
	auto := Runtime{
		profile: model.Profile{Name: "claude"},
		project: model.Project{Hash: "abc123abc123"},
		run:     model.RunOptions{Background: true},
		backend: &fakeBackend{},
	}
	named := auto
	named.run.SessionName = "1"
	if auto.containerName() != named.containerName() {
		t.Fatal("expected automatic and explicit names to select the same container")
	}
	if auto.sessionStartLockName() != named.sessionStartLockName() {
		t.Fatal("starts targeting the same container must share a start lock")
	}
}
