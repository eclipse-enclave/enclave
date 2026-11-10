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
	if req.NativeAuth != nil {
		own, running, err := b.InspectModeSession(ctx, req.Mode.HolderID, id)
		if err != nil || own != nil && (running || !waitContainerGone(ctx, own.ID)) {
			logx.Warnf("Session %s protected stores retained: agent removal could not be confirmed", req.Session.Name)
			return
		}
		sidecar, err := b.modeGateway(ctx, req.Mode.HolderID, req.Session.Name, req.Session.ProjectHash)
		if err != nil || sidecar != nil && !waitContainerGone(ctx, sidecar.ID) {
			logx.Warnf("Session %s protected stores retained: gateway removal could not be confirmed", req.Session.Name)
			return
		}
		dir, err := hoststore.ResolveDir(b.opts.Host.Home, req.Mode.ConfigStore, backend.StoreKindConfig)
		if err == nil {
			err = hoststore.WithModeHolders(b.opts.Host.Home, dir, func(holders *[]hoststore.ModeHolder) error {
				for i := range *holders {
					holder := &(*holders)[i]
					if holder.ID != req.Mode.HolderID {
						continue
					}
					if sidecar == nil && !holder.GatewayCorrelated {
						return fmt.Errorf("gateway correlation policy is missing")
					}
					holder.State, holder.UpdatedAt = "finalizing", time.Now().UTC()
					return nil
				}
				return fmt.Errorf("session mode holder is missing")
			})
		}
		if err != nil {
			logx.Warnf("Session %s protected stores retained: %v", req.Session.Name, err)
		}
		return
	}
	info, err := dockercmd.ContainerInspect(ctx, ref)
	if dockercmd.IsNotFound(err) || err == nil && info.State != nil && !info.State.Running {
		hoststore.AdvanceRequestMode(b.opts.Host.Home, req, "finalizing", id)
	} else {
		logx.Warnf("Session %s auth-mode reservation retained: container exit could not be confirmed", req.Session.Name)
	}
}

type modeFinalizer func(ended bool) error

func (finish modeFinalizer) release() {
	if err := finish(false); err != nil {
		logx.Warnf("Failed to relinquish auth finalization: %v", err)
	}
}

// beginContainerModeFinalization pins the holder to the inspected container ID.
// Stop/Remove cannot release a foreground owner's reservation: that process
// may still run its deferred auth sync after the engine reports the exit.
func (b *Backend) beginContainerModeFinalization(ctx context.Context, name string) (finish modeFinalizer, reconcile bool, remove func() error, err error) {
	noop := func(bool) error { return nil }
	remove = func() error { return RemoveSessionContainer(ctx, name, false) }
	info, err := dockercmd.ContainerInspect(ctx, name)
	if dockercmd.IsNotFound(err) {
		return noop, false, remove, nil
	}
	if err != nil {
		return nil, false, remove, err
	}
	dir := mountedSourceDir(info, containerEnvValue(info.Config, model.EnvToolConfigDir))
	if dir == "" || info.Config == nil || info.Config.Labels[model.LabelModeHolder] == "" {
		return noop, true, remove, nil
	}
	var selected string
	claimed := hoststore.ModeHolder{ID: info.Config.Labels[model.LabelModeHolder], Session: strings.TrimPrefix(info.Name, "/"), Tool: info.Config.Labels[model.LabelAgent], ProjectHash: info.Config.Labels[model.LabelHash], Protected: containerEnvValue(info.Config, model.EnvNativeAuthOps) != ""}
	reconcile = false
	err = hoststore.WithModeHolders(b.opts.Host.Home, dir, func(holders *[]hoststore.ModeHolder) error {
		for i := range *holders {
			holder := &(*holders)[i]
			if holder.ID != info.Config.Labels[model.LabelModeHolder] || holder.Engine != b.Name() ||
				holder.ContainerID != "" && holder.ContainerID != info.ID {
				continue
			}
			claimed = *holder
			if holder.State == "finalizing" && hoststore.ModeOwnerAlive(holder.OwnerPID) {
				return fmt.Errorf("session %s auth is being finalized by process %d", holder.Session, holder.OwnerPID)
			}
			if hoststore.ModeOwnerAlive(holder.OwnerPID) {
				reconcile = false
				return nil
			}
			reconcile = !holder.Protected
			holder.State, holder.UpdatedAt, holder.OwnerPID = "finalizing", time.Now().UTC(), os.Getpid()
			selected = holder.ID
			claimed = *holder
			break
		}
		return nil
	})
	if err != nil {
		return nil, false, remove, fmt.Errorf("reserve auth finalization: %w", err)
	}
	var nativeFinalize func() error
	if claimed.Protected {
		remove, nativeFinalize = b.protectedEnd(ctx, info, claimed)
	}
	return func(ended bool) error {
		if selected == "" {
			return nil
		}
		var err error
		if ended {
			err = hoststore.EndMode(b.opts.Host.Home, dir, selected, nativeFinalize)
		} else {
			err = hoststore.WithModeHolders(b.opts.Host.Home, dir, func(holders *[]hoststore.ModeHolder) error {
				for i := range *holders {
					holder := &(*holders)[i]
					if holder.ID == selected && holder.OwnerPID == os.Getpid() && holder.State == "finalizing" {
						holder.OwnerPID, holder.UpdatedAt = 0, time.Now().UTC()
					}
				}
				return nil
			})
		}
		if err == nil {
			selected = ""
		}
		return err
	}, reconcile, remove, nil
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
