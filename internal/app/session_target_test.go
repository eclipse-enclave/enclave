// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func session(name string, sessionName string, hash string, dir string) backend.Session {
	return backend.Session{
		Ref:         backend.SessionRef{Name: name},
		Tool:        "claude",
		ProjectHash: hash,
		Worktree:    dir,
		ProjectDir:  dir,
		Name:        sessionName,
		Status:      "running",
	}
}

func TestResolveSessionTargetAcceptsFullContainerName(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
	}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Requested: "enclave-claude-aaaaaaaaaaaa-my-task",
	})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-claude-aaaaaaaaaaaa-my-task" {
		t.Fatalf("resolved %q, want the container name it was given", got.Ref.Name)
	}
}

func TestResolveSessionTargetResolvesSessionName(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
	}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Requested: "my-task"})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-claude-aaaaaaaaaaaa-my-task" {
		t.Fatalf("resolved %q, want the container of session my-task", got.Ref.Name)
	}
}

func TestResolveSessionTargetSanitizesRequestedName(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
	}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Requested: "My Task"})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-claude-aaaaaaaaaaaa-my-task" {
		t.Fatalf("resolved %q, want the sanitized name to match", got.Ref.Name)
	}
}

func TestResolveSessionTargetPrefersCurrentProject(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
		session("enclave-claude-bbbbbbbbbbbb-my-task", "my-task", "bbbbbbbbbbbb", "/repo/b"),
	}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Requested: "my-task",
		Project:   model.Project{Hash: "bbbbbbbbbbbb", Dir: "/repo/b", RealDir: "/repo/b"},
	})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-claude-bbbbbbbbbbbb-my-task" {
		t.Fatalf("resolved %q, want the session of the current project", got.Ref.Name)
	}
}

func TestResolveSessionTargetResolvesAcrossProjectsWhenUnambiguous(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
	}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Requested: "my-task",
		Project:   model.Project{Hash: "bbbbbbbbbbbb", Dir: "/repo/b", RealDir: "/repo/b"},
	})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-claude-aaaaaaaaaaaa-my-task" {
		t.Fatalf("resolved %q, want the only session carrying the name", got.Ref.Name)
	}
}

func TestResolveSessionTargetReportsAmbiguousSessionName(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
		session("enclave-claude-bbbbbbbbbbbb-my-task", "my-task", "bbbbbbbbbbbb", "/repo/b"),
	}}

	_, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Requested: "my-task"})
	if err == nil {
		t.Fatal("resolveSessionTarget() must not guess between projects")
	}
	for _, want := range []string{"enclave-claude-aaaaaaaaaaaa-my-task", "enclave-claude-bbbbbbbbbbbb-my-task"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not list candidate %q", err, want)
		}
	}
}

func TestResolveSessionTargetReportsUnknownName(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
	}}

	_, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Requested: "other"})
	if err == nil || !strings.Contains(err.Error(), `"other"`) {
		t.Fatalf("error = %v, want it to name the unknown session", err)
	}
}

func TestResolveSessionTargetAutoSelectsWithinProject(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa", "", "aaaaaaaaaaaa", "/repo/a"),
		session("enclave-claude-bbbbbbbbbbbb-my-task", "my-task", "bbbbbbbbbbbb", "/repo/b"),
	}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Project: model.Project{Hash: "aaaaaaaaaaaa", Dir: "/repo/a", RealDir: "/repo/a"},
	})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-claude-aaaaaaaaaaaa" {
		t.Fatalf("resolved %q, want the running session of the current project", got.Ref.Name)
	}
}

func TestResolveSessionTargetListsOnlyRunningByDefault(t *testing.T) {
	be := &stopTestBackend{}

	if _, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Requested: "my-task"}); err == nil {
		t.Fatal("expected an error for an empty session list")
	}
	if !be.listFilter.RunningOnly || be.listFilter.All {
		t.Fatalf("filter = %+v, want running sessions only", be.listFilter)
	}

	if _, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Requested: "my-task", IncludeStopped: true}); err == nil {
		t.Fatal("expected an error for an empty session list")
	}
	if !be.listFilter.All || be.listFilter.RunningOnly {
		t.Fatalf("filter = %+v, want stopped sessions included", be.listFilter)
	}
}
