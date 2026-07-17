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
)

// fakeBackend is a minimal backend.Backend for exercising runtime code paths
// that only read session state. List returns the configured sessions (already
// filtered the way the real Docker backend would, i.e. gateway sidecars
// excluded); other methods are no-ops.
type fakeBackend struct {
	sessions []backend.Session
	listErr  error
	listFn   func(backend.SessionFilter) ([]backend.Session, error)

	storage        backend.StoreManager
	prepared       []backend.StorePrep
	prepareState   backend.StoreState
	configKeyInUse func(meta backend.SessionMeta, key string) (bool, error)
}

func (f *fakeBackend) Name() string                { return "fake" }
func (f *fakeBackend) Check(context.Context) error { return nil }
func (f *fakeBackend) Capabilities() backend.Capabilities {
	return backend.Capabilities{RestrictedNetwork: true, SecretHTTPRelease: true}
}
func (f *fakeBackend) Storage() backend.StoreManager { return f.storage }
func (f *fakeBackend) PrepareStores(_ context.Context, prep backend.StorePrep) (backend.StoreState, error) {
	f.prepared = append(f.prepared, prep)
	return f.prepareState, nil
}
func (f *fakeBackend) Run(context.Context, backend.Request, backend.AttachIO) (backend.ExitStatus, error) {
	return backend.ExitStatus{}, nil
}
func (f *fakeBackend) Start(context.Context, backend.Request) (backend.SessionRef, error) {
	return backend.SessionRef{}, nil
}
func (f *fakeBackend) List(_ context.Context, filter backend.SessionFilter) ([]backend.Session, error) {
	if f.listFn != nil {
		return f.listFn(filter)
	}
	return f.sessions, f.listErr
}
func (f *fakeBackend) Inspect(context.Context, backend.SessionRef) (*backend.Session, error) {
	return nil, nil
}
func (f *fakeBackend) Attach(context.Context, backend.SessionRef, backend.AttachIO) error { return nil }
func (f *fakeBackend) Stop(context.Context, backend.SessionRef, backend.StopOptions) error {
	return nil
}
func (f *fakeBackend) Remove(context.Context, backend.SessionRef) error { return nil }

func (f *fakeBackend) ConfigStoreKeyInUse(_ context.Context, meta backend.SessionMeta, key string) (bool, error) {
	if f.configKeyInUse == nil {
		return false, nil
	}
	return f.configKeyInUse(meta, key)
}
