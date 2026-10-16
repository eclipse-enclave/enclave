// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"enclave/internal/backend"
	"enclave/internal/model"
	"errors"
	"os"
	"testing"
)

type terminalBackend struct {
	*fakeBackend
	active  *bool
	t       *testing.T
	failure error
}

func (b *terminalBackend) Run(_ context.Context, _ backend.Request, attach backend.AttachIO) (backend.ExitStatus, error) {
	if !*b.active {
		b.t.Fatal("backend run began outside terminal scope")
	}
	attach.OnStarted()
	if !*b.active {
		b.t.Fatal("OnStarted prematurely ended scope")
	}
	return backend.ExitStatus{}, b.failure
}
func (b *terminalBackend) Exec(context.Context, backend.SessionRef, backend.ExecRequest, backend.AttachIO) error {
	if !*b.active {
		b.t.Fatal("backend exec began outside terminal scope")
	}
	return b.failure
}
func TestRunTerminalPreservesStartLockAndFailure(t *testing.T) {
	active := false
	closes, releases := 0, 0
	failure := &backend.ExitError{Code: 17}
	be := &terminalBackend{fakeBackend: &fakeBackend{}, active: &active, t: t, failure: failure}
	r := &Runtime{backend: be, profile: model.Profile{Name: "claude", Command: "claude"}, project: model.Project{Dir: t.TempDir()}}
	r.terminalLifecycle = func(_ context.Context, target model.TerminalTarget, handled ...os.Signal) func(error) {
		if target.Name != "test-session" || len(handled) != 2 {
			t.Fatal(target, handled)
		}
		active = true
		return func(err error) {
			if !errors.Is(err, failure) || releases != 1 {
				t.Fatal("lost exit or start lock", err, releases)
			}
			active = false
			closes++
		}
	}
	if err := r.runContainer(context.Background(), &ExecutionContext{ContainerName: "test-session"}, func() { releases++ }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if active || closes != 1 || releases != 1 {
		t.Fatal(active, closes, releases)
	}
}
func TestExecTerminalUsesValidatedActualMetadata(t *testing.T) {
	active := false
	closes := 0
	failure := &backend.ExitError{Code: 23}
	session := backend.Session{Ref: backend.SessionRef{Name: "existing", ID: "actual-id"}, Tool: "codex", ProjectDir: "/target/project", ProjectHash: "target-hash"}
	be := &terminalBackend{fakeBackend: &fakeBackend{sessions: []backend.Session{session}}, active: &active, t: t, failure: failure}
	r := &Runtime{backend: be, profile: model.Profile{Name: "claude"}, project: model.Project{Dir: "/invocation/project"}}
	r.terminalLifecycle = func(_ context.Context, target model.TerminalTarget, _ ...os.Signal) func(error) {
		if target.Tool != "codex" || target.ProjectDir != "/target/project" || target.ID != "actual-id" {
			t.Fatal(target)
		}
		active = true
		return func(err error) {
			if !errors.Is(err, failure) {
				t.Fatal(err)
			}
			active = false
			closes++
		}
	}
	if err := r.Exec(ExecTarget{Name: "existing"}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if active || closes != 1 {
		t.Fatal(active, closes)
	}
	be.sessions = nil
	if err := r.Exec(ExecTarget{Name: "missing"}); err == nil || closes != 1 {
		t.Fatal("invalid target entered terminal scope", err, closes)
	}
}
