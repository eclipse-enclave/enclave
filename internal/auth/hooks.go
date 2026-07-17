// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package auth

import (
	"sync"

	"enclave/internal/backend"
	"enclave/internal/model"
)

// Context carries what tool auth hooks need to prepare credentials. Hooks
// operate on persistent stores through Storage using the neutral store
// identities; they never see backend-specific store names.
type Context struct {
	Host    model.Host
	Project model.Project
	Profile model.Profile
	Run     model.RunOptions
	Auth    model.AuthOptions
	Build   model.BuildOptions

	// Storage performs store operations; nil in minimal test setups.
	Storage backend.StoreManager
	// ConfigStore is the session's config store; nil when the profile has none.
	ConfigStore *backend.StoreRef
	// AuthStorage is where session credentials live: the shared auth store
	// when present, the config store otherwise.
	AuthStorage *backend.StoreRef
	// StorageHasSession reports whether a provider session was found in
	// AuthStorage.
	StorageHasSession bool
}

type Hooks interface {
	OnAuthReady(ctx Context) (bool, error)
	AfterEnvInjected(ctx Context, injected map[string]string) error
	FinalizeAuth(ctx Context, state model.AuthState) error
}

type noopHooks struct{}

func (noopHooks) OnAuthReady(Context) (bool, error) { return false, nil }
func (noopHooks) AfterEnvInjected(Context, map[string]string) error {
	return nil
}
func (noopHooks) FinalizeAuth(Context, model.AuthState) error { return nil }

var (
	hooksMu     sync.RWMutex
	hooksByTool = map[string]Hooks{}
)

func RegisterHooks(tool string, hooks Hooks) {
	if tool == "" || hooks == nil {
		return
	}
	hooksMu.Lock()
	hooksByTool[tool] = hooks
	hooksMu.Unlock()
}

func HooksFor(profile model.Profile) Hooks {
	hooksMu.RLock()
	hooks := hooksByTool[profile.Name]
	hooksMu.RUnlock()
	if hooks == nil {
		return noopHooks{}
	}
	return hooks
}
