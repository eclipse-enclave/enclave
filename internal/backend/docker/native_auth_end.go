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
	"path/filepath"
	"strings"
	"time"

	"enclave/internal/backend"
	"enclave/internal/backend/hoststore"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/gateway"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/placeholderstore"
)

func (b *Backend) modeGateway(ctx context.Context, holderID, session, hash string) (*dockercmd.InspectResponse, error) {
	if holderID == "" || session == "" || hash == "" {
		return nil, fmt.Errorf("protected gateway ownership is incomplete")
	}
	filters := dockercmd.NewFilters()
	filters.Add("label", model.GatewayLabelModeHolder+"="+holderID)
	filters.Add("label", model.GatewayLabelManaged+"=true")
	matches, err := dockercmd.ContainerList(ctx, dockercmd.ListOptions{All: true, Filters: filters})
	if err != nil || len(matches) == 0 {
		return nil, err
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("multiple gateways claim protected holder %s", holderID)
	}
	info, err := dockercmd.ContainerInspect(ctx, matches[0].ID)
	if err != nil {
		return nil, err
	}
	if !gateway.ContainerOwnedBy(info, session, hash) || info.Config.Labels[model.GatewayLabelModeHolder] != holderID {
		return nil, fmt.Errorf("gateway does not match protected holder %s", holderID)
	}
	return &info, nil
}

func removeModeGateway(ctx context.Context, info dockercmd.InspectResponse) error {
	ctx, cancel := context.WithTimeout(ctx, endedContainerGoneTimeout)
	defer cancel()
	owner, hash := info.Config.Labels[model.GatewayLabelContainer], info.Config.Labels[model.GatewayLabelProjectHash]
	network, hasNetwork := captureSessionNetworkRef(ctx, owner, hash)
	timeout := 3 * time.Second
	_ = dockercmd.ContainerStop(ctx, info.ID, &timeout)
	_ = dockercmd.ContainerRemove(ctx, info.ID, true, true)
	if !waitContainerGone(ctx, info.ID) {
		return fmt.Errorf("protected gateway removal could not be confirmed")
	}
	if hasNetwork {
		cleanupSessionNetwork(network)
	}
	return nil
}

// RemoveModeGateway never resolves an agent by a reused name. The caller decides
// whether zero matches are absence evidence for this holder's creation protocol.
func (b *Backend) RemoveModeGateway(ctx context.Context, holderID, session, hash string) (bool, error) {
	info, err := b.modeGateway(ctx, holderID, session, hash)
	if err != nil || info == nil {
		return false, err
	}
	return true, removeModeGateway(ctx, *info)
}

// Capture the original gateway before removing the agent. Engine teardown is
// independent of authority to harvest or end the host reservation.
func (b *Backend) protectedEnd(ctx context.Context, agent dockercmd.InspectResponse, holder hoststore.ModeHolder) (func() error, func() error) {
	sidecar, evidenceErr := b.modeGateway(ctx, holder.ID, holder.Session, holder.ProjectHash)
	if evidenceErr == nil && sidecar == nil {
		evidenceErr = fmt.Errorf("protected gateway correlation is missing")
	}
	remove := func() error {
		if err := dockercmd.ContainerRemove(ctx, agent.ID, true, false); err != nil && !dockercmd.IsNotFound(err) {
			return err
		}
		if evidenceErr != nil {
			return evidenceErr
		}
		return removeModeGateway(ctx, *sidecar)
	}
	finalize := func() error {
		if evidenceErr != nil {
			return evidenceErr
		}
		if holder.Ephemeral {
			return nil
		}
		native := mountedSourceDir(*sidecar, model.GatewayNativeAuthStorePath)
		configDir := mountedSourceDir(agent, containerEnvValue(agent.Config, model.EnvToolConfigDir))
		view := mountedSourceDir(agent, containerEnvValue(agent.Config, model.EnvAuthDir))
		views := filepath.Join(config.HostProjectToolDir(b.opts.Host.Home, holder.ProjectHash, holder.Tool), "auth-views")
		expected, err := hoststore.ResolveDir(b.opts.Host.Home, backend.StoreKey{Owner: holder.Tool, Suffix: filepath.Base(native)}, backend.StoreKindAuth)
		if native == "" || err != nil || expected != native || filepath.Dir(view) != views || !strings.HasPrefix(filepath.Base(view), holder.ID+"-") {
			logx.Warnf("Skipping protected metadata for %s: native store or own view evidence is unavailable", holder.Session)
			return nil
		}
		profile, err := config.LoadProfile(b.opts.Paths, holder.Tool)
		if err == nil {
			err = hoststore.WithLock(b.opts.Host.Home, native, func() error { return placeholderstore.Harvest(native, view, configDir, profile) })
		}
		if err != nil {
			logx.Warnf("Skipping protected metadata for %s: %v", holder.Session, err)
		}
		return nil
	}
	return remove, finalize
}
