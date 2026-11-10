// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"time"

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
		ProjectHash: r.project.Hash, Engine: r.backend.Name(), OwnerPID: os.Getpid(), Protected: r.run.ProtectAuth}
	var discover func([]hoststore.ModeHolder) error
	if r.run.ProtectAuth {
		checker, ok := r.backend.(backend.ProtectedAuthAdmission)
		if !ok {
			return nil, fmt.Errorf("backend %s cannot admit protected auth", r.backend.Name())
		}
		discover = func(holders []hoststore.ModeHolder) error {
			approved := make(map[string]string)
			for _, holder := range holders {
				approved[holder.ID] = holder.Engine
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return checker.CheckProtectedAuthAdmission(ctx, key, approved)
		}
	}
	if err := hoststore.ReserveMode(r.host.Home, dir, holder, discover); err != nil {
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
		err = hoststore.AdvanceMode(r.host.Home, dir, mode.HolderID, "finalizing", "", false)
	}
	if err == nil {
		err = hoststore.AdvanceMode(r.host.Home, dir, mode.HolderID, "ended", "", false)
	}
	if err != nil {
		logx.Warnf("Session auth-mode reservation retained: %v", err)
	}
}
