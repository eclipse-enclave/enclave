// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package hostlayout realizes persistent stores directly on the shared host
// layout (internal/backend/hoststore) for backends that mount host
// directories into the session instead of using Docker volumes: store
// preparation, mount sources, and post-session auth reconciliation.
package hostlayout

import (
	"enclave/internal/backend"
	"enclave/internal/model"
)

// Layout is the host-layout store implementation shared by the qemu and sbx
// backends.
type Layout struct {
	host    model.Host
	storage *StoreManager
}

// New returns a Layout rooted at the host's home directory.
func New(host model.Host) *Layout {
	return &Layout{host: host, storage: newStoreManager(host)}
}

// Storage returns the store manager backing this layout.
func (l *Layout) Storage() backend.StoreManager { return l.storage }

// MountSource returns the host directory that backs a persistent store,
// creating it when missing.
func (l *Layout) MountSource(key backend.StoreKey, kind backend.StoreKind) (string, error) {
	return l.storage.MountSource(key, kind)
}
