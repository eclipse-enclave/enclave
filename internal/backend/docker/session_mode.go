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
	"strings"
	"time"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	dockercmd "enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
)

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
		hoststore.AdvanceRequestMode(b.opts.Host.Home, req, "finalizing", id)
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
	reconcile = false
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
			reconcile = true
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
		return hoststore.AdvanceMode(b.opts.Host.Home, dir, selected, "ended", "", false)
	}, reconcile, nil
}

// InspectModeSession finds pending containers by the host reservation label,
// even if the host died before saving the container ID or it was renamed.
func (b *Backend) InspectModeSession(ctx context.Context, holderID, id string) (*backend.SessionRef, bool, error) {
	if id == "" {
		filters := dockercmd.NewFilters()
		filters.Add("label", model.LabelModeHolder+"="+holderID)
		containers, err := dockercmd.ContainerList(ctx, dockercmd.ListOptions{All: true, Filters: filters})
		if err != nil || len(containers) == 0 {
			return nil, false, err
		}
		if len(containers) != 1 {
			return nil, false, fmt.Errorf("multiple containers claim this holder")
		}
		id = containers[0].ID
	}
	info, err := dockercmd.ContainerInspect(ctx, id)
	if dockercmd.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.State == nil || info.Config == nil || info.Config.Labels[model.LabelModeHolder] != holderID {
		return nil, false, fmt.Errorf("container does not match the auth-mode holder")
	}
	return &backend.SessionRef{ID: info.ID, Name: strings.TrimPrefix(info.Name, "/")}, info.State.Running, nil
}
