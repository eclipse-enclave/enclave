// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"fmt"
	"os"
	"time"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	dockercmd "enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
)

func (b *Backend) advanceRequestMode(req backend.Request, state, id string) {
	if req.Mode == nil {
		return
	}
	dir, err := b.storage.storeDir(req.Mode.ConfigStore, backend.StoreKindConfig)
	if err == nil {
		err = hoststore.AdvanceMode(b.opts.Host.Home, dir, req.Mode.HolderID, state, id, "", req.Detached)
	}
	if err != nil {
		// The pending/live record still excludes opposite-mode starts. Never
		// clear it just because an update or an engine inspection failed.
		logx.Warnf("Session auth-mode reservation retained: %v", err)
	}
}

func (b *Backend) finalizingRequestMode(req backend.Request, id string) {
	if req.Mode == nil {
		return
	}
	ref := id
	if ref == "" {
		ref = req.Session.Name
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := dockercmd.ContainerInspect(ctx, ref)
	if dockercmd.IsNotFound(err) || err == nil && info.State != nil && !info.State.Running {
		b.advanceRequestMode(req, "finalizing", id)
	} else {
		logx.Warnf("Session %s auth-mode reservation retained: container exit could not be confirmed", req.Session.Name)
	}
}

// beginContainerModeFinalization pins the holder to the inspected container ID.
// Stop/Remove cannot release a foreground owner's reservation: that process
// may still run its deferred auth sync after the engine reports the exit.
func (b *Backend) beginContainerModeFinalization(ctx context.Context, name string) (finish func() error, reconcile bool, err error) {
	noop := func() error { return nil }
	info, err := dockercmd.ContainerInspect(ctx, name)
	if dockercmd.IsNotFound(err) {
		return noop, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	dir := mountedSourceDir(info, containerEnvValue(info.Config, model.EnvToolConfigDir))
	if dir == "" || info.Config == nil || info.Config.Labels[model.LabelModeHolder] == "" {
		return noop, true, nil
	}
	var selected string
	reconcile = true
	err = hoststore.WithModeHolders(b.opts.Host.Home, dir, func(holders *[]hoststore.ModeHolder) error {
		for i := range *holders {
			holder := &(*holders)[i]
			if holder.ID != info.Config.Labels[model.LabelModeHolder] || holder.Engine != b.Name() ||
				holder.ContainerID != "" && holder.ContainerID != info.ID {
				continue
			}
			if holder.State == "finalizing" && hoststore.ModeOwnerAlive(holder.OwnerPID) {
				return fmt.Errorf("session %s auth is being finalized by process %d", holder.Session, holder.OwnerPID)
			}
			if holder.State == "ended" || hoststore.ModeOwnerAlive(holder.OwnerPID) {
				reconcile = false
				return nil
			}
			holder.State, holder.UpdatedAt, holder.OwnerPID = "finalizing", time.Now().UTC(), os.Getpid()
			selected = holder.ID
			break
		}
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("reserve auth finalization: %w", err)
	}
	return func() error {
		if selected == "" {
			return nil
		}
		return hoststore.AdvanceMode(b.opts.Host.Home, dir, selected, "ended", "", "", false)
	}, reconcile, nil
}
