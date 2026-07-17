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
	"testing"

	"enclave/internal/backend"
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

type stopTestBackend struct {
	stopErr            error
	stopCalled         bool
	stopOpts           backend.StopOptions
	forceRemoveCalled  bool
	strictRemoveCalled bool
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
func (b *stopTestBackend) List(context.Context, backend.SessionFilter) ([]backend.Session, error) {
	return nil, nil
}
func (b *stopTestBackend) Inspect(context.Context, backend.SessionRef) (*backend.Session, error) {
	return nil, nil
}
func (b *stopTestBackend) Attach(context.Context, backend.SessionRef, backend.AttachIO) error {
	return nil
}
func (b *stopTestBackend) Stop(_ context.Context, _ backend.SessionRef, opts backend.StopOptions) error {
	b.stopCalled = true
	b.stopOpts = opts
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
