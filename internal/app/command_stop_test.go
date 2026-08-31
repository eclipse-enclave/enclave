// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestStopContainerFinalizesThenForceRemovesOnFinalizeError(t *testing.T) {
	be := &stopTestBackend{stopErr: errors.New("finalize failed")}

	stopContainer(be, "managed")

	if !be.stopCalled {
		t.Fatal("Stop was not called")
	}
	if !be.stopOpts.Finalize {
		t.Fatal("Stop must request auth finalization")
	}
	if !be.forceRemoveCalled {
		t.Fatal("RemoveWithoutFinalize must run even when finalization fails")
	}
	if be.strictRemoveCalled {
		t.Fatal("strict Remove should not be used when unsafe remove is available")
	}
}

func TestStopSessionsRequiresANameToStopSomething(t *testing.T) {
	sessions := []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
		session("enclave-claude-aaaaaaaaaaaa-other", "other", "aaaaaaaaaaaa", "/repo/a"),
	}
	tests := []struct {
		name     string
		opts     model.Options
		sessions []backend.Session
		code     int
		stopped  []string
	}{
		{
			name:     "blank --name stops nothing",
			opts:     stopOptionsWithName("   "),
			sessions: sessions,
		},
		{
			name:     "unsanitizable --name stops nothing",
			opts:     stopOptionsWithName("???"),
			sessions: sessions,
		},
		{
			// The single session would be auto-selected if the blank argument were
			// read as "no argument".
			name:     "blank positional argument is rejected",
			opts:     stopOptionsWithArg("   "),
			sessions: sessions[:1],
			code:     1,
		},
		{
			name:     "no filter stops every background session",
			opts:     model.Options{Sources: model.DefaultOptionSources()},
			sessions: sessions,
			stopped:  []string{sessions[0].Ref.Name, sessions[1].Ref.Name},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			be := &stopTestBackend{sessions: tt.sessions}
			if code := stopSessions(context.Background(), be, tt.opts, ""); code != tt.code {
				t.Fatalf("stopSessions() = %d, want %d", code, tt.code)
			}
			if !slices.Equal(be.stopped, tt.stopped) {
				t.Fatalf("stopped %v, want %v", be.stopped, tt.stopped)
			}
		})
	}
}

func stopOptionsWithName(sessionName string) model.Options {
	opts := model.Options{Sources: model.DefaultOptionSources()}
	opts.SessionName = sessionName
	opts.Sources.SessionName = model.SourceCLI
	return opts
}

func stopOptionsWithArg(arg string) model.Options {
	opts := model.Options{Sources: model.DefaultOptionSources()}
	opts.CmdArgs = []string{arg}
	return opts
}

type stopTestBackend struct {
	stopErr            error
	stopCalled         bool
	stopOpts           backend.StopOptions
	stopped            []string
	forceRemoveCalled  bool
	strictRemoveCalled bool
	sessions           []backend.Session
	listFilter         backend.SessionFilter
}

func (b *stopTestBackend) Name() string { return "test" }
func (b *stopTestBackend) Check(context.Context) error {
	return nil
}
func (b *stopTestBackend) Capabilities() backend.Capabilities {
	return backend.Capabilities{}
}
func (b *stopTestBackend) Storage() backend.StoreManager {
	return nil
}
func (b *stopTestBackend) PrepareStores(context.Context, backend.StorePrep) (backend.StoreState, error) {
	return backend.StoreState{}, nil
}
func (b *stopTestBackend) Run(context.Context, backend.Request, backend.AttachIO) (backend.ExitStatus, error) {
	return backend.ExitStatus{}, nil
}
func (b *stopTestBackend) Start(context.Context, backend.Request) (backend.SessionRef, error) {
	return backend.SessionRef{}, nil
}
func (b *stopTestBackend) List(_ context.Context, filter backend.SessionFilter) ([]backend.Session, error) {
	b.listFilter = filter
	return b.sessions, nil
}
func (b *stopTestBackend) Inspect(context.Context, backend.SessionRef) (*backend.Session, error) {
	return nil, nil
}
func (b *stopTestBackend) Attach(context.Context, backend.SessionRef, backend.AttachIO) error {
	return nil
}
func (b *stopTestBackend) Stop(_ context.Context, ref backend.SessionRef, opts backend.StopOptions) error {
	b.stopCalled = true
	b.stopOpts = opts
	b.stopped = append(b.stopped, ref.Name)
	return b.stopErr
}
func (b *stopTestBackend) Remove(context.Context, backend.SessionRef) error {
	b.strictRemoveCalled = true
	return nil
}
func (b *stopTestBackend) RemoveWithoutFinalize(context.Context, backend.SessionRef) error {
	b.forceRemoveCalled = true
	return nil
}
