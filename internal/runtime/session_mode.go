// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"crypto/rand"
	"os"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/logx"
)

func (r *Runtime) reserveSessionMode(name string) (*backend.SessionMode, error) {
	if r.backend == nil {
		return nil, nil
	}
	key := backend.StoreKey{Owner: r.profile.Name, ProjectHash: r.project.Hash, Suffix: r.configVolSuffix}
	dir, err := hoststore.ResolveDir(r.host.Home, key, backend.StoreKindConfig)
	if err != nil {
		return nil, err
	}
	mode := &backend.SessionMode{ConfigStore: key, HolderID: rand.Text()}
	holder := hoststore.ModeHolder{ID: mode.HolderID, Session: name, Tool: r.profile.Name,
		ProjectHash: r.project.Hash, Engine: r.backend.Name(), OwnerPID: os.Getpid()}
	if err := hoststore.ReserveMode(r.host.Home, dir, holder, nil); err != nil {
		return nil, err
	}
	return mode, nil
}

func (r *Runtime) endSessionMode(mode *backend.SessionMode, unstarted bool) {
	if mode == nil {
		return
	}
	dir, err := hoststore.ResolveDir(r.host.Home, mode.ConfigStore, backend.StoreKindConfig)
	if err == nil && unstarted {
		err = hoststore.AdvanceMode(r.host.Home, dir, mode.HolderID, "finalizing", "", "", false)
	}
	if err == nil {
		err = hoststore.AdvanceMode(r.host.Home, dir, mode.HolderID, "ended", "", "", false)
	}
	if err != nil {
		logx.Warnf("Session auth-mode reservation retained: %v", err)
	}
}
