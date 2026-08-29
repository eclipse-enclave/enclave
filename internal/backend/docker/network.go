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
	"strconv"
	"strings"
	"time"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/logx"
	"enclave/internal/model"
)

const (
	sessionNetworkDynamicSubnet   = "0.0.0.0/28"
	sessionNetworkGCGracePeriod   = time.Hour
	sessionNetworkEnsureAttempts  = 4
	sessionNetworkRemoveRetries   = 5
	sessionNetworkRemoveRetryWait = 100 * time.Millisecond
	hostBindingIPv4Option         = "com.docker.network.bridge.host_binding_ipv4"
)

var (
	dockerInfo             = dockercmd.Info
	networkCreate          = dockercmd.NetworkCreate
	networkInspect         = dockercmd.NetworkInspect
	networkList            = dockercmd.NetworkList
	networkRemove          = dockercmd.NetworkRemove
	sessionContainerExists = dockerSessionContainerExists
	sessionRuntimeExists   = dockerSessionRuntimeExists
)

type sessionNetworkRef struct {
	Name        string
	ID          string
	Container   string
	ProjectHash string
}

type sessionNetworkAction int

const (
	sessionNetworkReuse sessionNetworkAction = iota
	sessionNetworkConflict
	sessionNetworkAttached
)

func sessionNetworkName(containerName string) string {
	return strings.TrimSpace(containerName) + model.SessionNetworkSuffix
}

func sessionNetworkLabels(meta backend.SessionMeta) map[string]string {
	return map[string]string{
		model.NetworkLabelManaged:     "true",
		model.NetworkLabelContainer:   strings.TrimSpace(meta.Name),
		model.NetworkLabelProjectHash: strings.TrimSpace(meta.ProjectHash),
	}
}

func sessionNetworkActionFor(info dockercmd.NetworkInspectResponse, meta backend.SessionMeta) sessionNetworkAction {
	labels := info.Labels
	if !strings.EqualFold(strings.TrimSpace(labels[model.NetworkLabelManaged]), "true") ||
		strings.TrimSpace(labels[model.NetworkLabelContainer]) != strings.TrimSpace(meta.Name) ||
		strings.TrimSpace(labels[model.NetworkLabelProjectHash]) != strings.TrimSpace(meta.ProjectHash) {
		return sessionNetworkConflict
	}
	if len(info.Containers) > 0 {
		return sessionNetworkAttached
	}
	return sessionNetworkReuse
}

func (b *Backend) ensureSessionNetwork(ctx context.Context, meta backend.SessionMeta, serverVersion string) (sessionNetworkRef, error) {
	name := sessionNetworkName(meta.Name)
	if strings.TrimSpace(meta.Name) == "" {
		return sessionNetworkRef{}, fmt.Errorf("cannot create a per-session network without a container name")
	}

	for attempt := 0; attempt < sessionNetworkEnsureAttempts; attempt++ {
		info, err := networkInspect(ctx, name)
		if err == nil {
			action := sessionNetworkActionFor(info, meta)
			switch action {
			case sessionNetworkReuse:
				ref, err := inspectedSessionNetworkRef(info, meta)
				if err != nil {
					return sessionNetworkRef{}, err
				}
				return ref, nil
			case sessionNetworkConflict:
				return sessionNetworkRef{}, fmt.Errorf("docker network %q already exists and is not owned by this enclave session", name)
			case sessionNetworkAttached:
				return sessionNetworkRef{}, attachedSessionNetworkError(meta.Name, name)
			}
		} else if !dockercmd.IsNotFound(err) {
			return sessionNetworkRef{}, fmt.Errorf("inspect per-session network %q: %w", name, err)
		}

		id, err := createSessionNetwork(ctx, meta, serverVersion)
		if err != nil {
			if dockercmd.IsAlreadyExists(err) {
				continue
			}
			if dockercmd.IsAddressPoolExhausted(err) {
				return sessionNetworkRef{}, fmt.Errorf("create per-session network %q: Docker address pools are exhausted; upgrade the Docker daemon to 29.0.0 or newer, or configure daemon default-address-pools: %w", name, err)
			}
			return sessionNetworkRef{}, fmt.Errorf("create per-session network %q: %w", name, err)
		}
		if strings.TrimSpace(id) == "" {
			return sessionNetworkRef{}, fmt.Errorf("create per-session network %q: Docker returned an empty network ID", name)
		}
		return sessionNetworkRef{Name: name, ID: id, Container: meta.Name, ProjectHash: meta.ProjectHash}, nil
	}
	return sessionNetworkRef{}, fmt.Errorf("create per-session network %q: repeated concurrent changes prevented ownership verification", name)
}

func createSessionNetwork(ctx context.Context, meta backend.SessionMeta, serverVersion string) (string, error) {
	ipv6 := false
	opts := dockercmd.NetworkCreateOptions{
		Name:       sessionNetworkName(meta.Name),
		Driver:     "bridge",
		EnableIPv6: &ipv6,
		Labels:     sessionNetworkLabels(meta),
		Options: map[string]string{
			hostBindingIPv4Option: "127.0.0.1",
		},
	}
	if dockerVersionAtLeast(serverVersion, 29, 0) {
		opts.Subnet = sessionNetworkDynamicSubnet
	}

	id, err := networkCreate(ctx, opts)
	if err == nil || dockercmd.IsAlreadyExists(err) || opts.Subnet == "" {
		return id, err
	}

	// A daemon or CLI that reports a new-enough version can still reject the
	// unspecified-address subnet request. Fall back to Docker's default IPAM.
	opts.Subnet = ""
	return networkCreate(ctx, opts)
}

func dockerVersionAtLeast(version string, wantMajor int, wantMinor int) bool {
	version = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(version), "v"))
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(numericPrefix(parts[0]))
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(numericPrefix(parts[1]))
	if err != nil {
		return false
	}
	return major > wantMajor || major == wantMajor && minor >= wantMinor
}

func numericPrefix(value string) string {
	for i, r := range value {
		if r < '0' || r > '9' {
			return value[:i]
		}
	}
	return value
}

func sessionNetworkPastGracePeriod(info dockercmd.NetworkInspectResponse, now time.Time) bool {
	created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(info.Created))
	return err == nil && !created.After(now.Add(-sessionNetworkGCGracePeriod))
}

func inspectedSessionNetworkRef(info dockercmd.NetworkInspectResponse, meta backend.SessionMeta) (sessionNetworkRef, error) {
	id := strings.TrimSpace(info.ID)
	if id == "" {
		return sessionNetworkRef{}, fmt.Errorf("docker network %q has no inspect ID", info.Name)
	}
	return sessionNetworkRef{
		Name:        sessionNetworkName(meta.Name),
		ID:          id,
		Container:   strings.TrimSpace(meta.Name),
		ProjectHash: strings.TrimSpace(meta.ProjectHash),
	}, nil
}

func attachedSessionNetworkError(containerName string, networkName string) error {
	return fmt.Errorf("session %s already running or requires stale-resource cleanup: Docker network %q has attached endpoints; run 'enclave stop %s' and retry", containerName, networkName, containerName)
}

func (b *Backend) cleanupSessionNetwork(ref sessionNetworkRef) {
	if strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.ID) == "" || strings.TrimSpace(ref.Container) == "" {
		return
	}
	ctx := context.Background()
	if err := removeOwnedSessionNetwork(ctx, ref, sessionNetworkRemoveRetries); err != nil {
		logx.Warnf("Failed to remove per-session network for %s: %v", ref.Container, err)
	}
}

func removeOwnedSessionNetwork(ctx context.Context, ref sessionNetworkRef, retries int) error {
	for attempt := 0; attempt <= retries; attempt++ {
		info, err := networkInspect(ctx, ref.Name)
		if err != nil {
			if dockercmd.IsNotFound(err) {
				return nil
			}
			return err
		}
		if strings.TrimSpace(info.ID) != strings.TrimSpace(ref.ID) ||
			!strings.EqualFold(strings.TrimSpace(info.Labels[model.NetworkLabelManaged]), "true") ||
			strings.TrimSpace(info.Labels[model.NetworkLabelContainer]) != strings.TrimSpace(ref.Container) ||
			strings.TrimSpace(info.Labels[model.NetworkLabelProjectHash]) != strings.TrimSpace(ref.ProjectHash) {
			return nil
		}
		if len(info.Containers) > 0 {
			if attempt < retries {
				time.Sleep(sessionNetworkRemoveRetryWait)
				continue
			}
			logx.Debugf("Leaving per-session network %s in place because it still has attached endpoints", ref.Name)
			return nil
		}
		exists, err := sessionRuntimeExists(ctx, ref.Container)
		if err != nil {
			return err
		}
		if exists {
			if attempt < retries {
				time.Sleep(sessionNetworkRemoveRetryWait)
				continue
			}
			logx.Debugf("Leaving per-session network %s in place because a session or gateway container still reserves its name", ref.Name)
			return nil
		}
		removeRef, err := inspectedNetworkRemoveRef(info)
		if err != nil {
			return err
		}
		if err := networkRemove(ctx, removeRef); err != nil {
			if dockercmd.IsNotFound(err) {
				return nil
			}
			if dockercmd.IsActiveEndpoints(err) && attempt < retries {
				time.Sleep(sessionNetworkRemoveRetryWait)
				continue
			}
			return err
		}
		return nil
	}
	return nil
}

func inspectedNetworkRemoveRef(info dockercmd.NetworkInspectResponse) (string, error) {
	if id := strings.TrimSpace(info.ID); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("docker network %q has no inspect ID; refusing name-based removal", info.Name)
}

func dockerSessionRuntimeExists(ctx context.Context, containerName string) (bool, error) {
	for _, name := range []string{containerName, strings.TrimSpace(containerName) + model.GatewayContainerSuffix} {
		_, err := dockercmd.ContainerInspect(ctx, name)
		if err == nil {
			return true, nil
		}
		if !dockercmd.IsNotFound(err) {
			return false, err
		}
	}
	return false, nil
}

func captureSessionNetworkRef(ctx context.Context, containerName string) (sessionNetworkRef, bool) {
	name := sessionNetworkName(containerName)
	info, err := networkInspect(ctx, name)
	if err != nil {
		if !dockercmd.IsNotFound(err) {
			logx.Debugf("Failed to capture per-session network %s before teardown: %v", name, err)
		}
		return sessionNetworkRef{}, false
	}
	labels := info.Labels
	if strings.TrimSpace(info.ID) == "" || info.Name != name ||
		!strings.EqualFold(strings.TrimSpace(labels[model.NetworkLabelManaged]), "true") ||
		strings.TrimSpace(labels[model.NetworkLabelContainer]) != strings.TrimSpace(containerName) {
		return sessionNetworkRef{}, false
	}
	return sessionNetworkRef{
		Name:        name,
		ID:          strings.TrimSpace(info.ID),
		Container:   strings.TrimSpace(containerName),
		ProjectHash: strings.TrimSpace(labels[model.NetworkLabelProjectHash]),
	}, true
}

func (b *Backend) gcSessionNetworks(ctx context.Context, now time.Time) {
	filters := dockercmd.NewFilters()
	filters.Add("label", model.NetworkLabelManaged+"=true")
	networks, err := networkList(ctx, filters)
	if err != nil {
		logx.Debugf("Failed to list stale per-session networks: %v", err)
		return
	}
	for _, info := range networks {
		owner, ok := staleSessionNetworkOwner(info, now)
		if !ok {
			continue
		}
		exists, err := sessionContainerExists(ctx, owner)
		if err != nil || exists {
			continue
		}
		if strings.TrimSpace(info.ID) == "" {
			continue
		}
		ref := sessionNetworkRef{
			Name:        info.Name,
			ID:          info.ID,
			Container:   owner,
			ProjectHash: strings.TrimSpace(info.Labels[model.NetworkLabelProjectHash]),
		}
		if err := removeOwnedSessionNetwork(ctx, ref, 0); err != nil {
			logx.Debugf("Failed to remove stale per-session network %s: %v", info.Name, err)
		}
	}
}

func dockerSessionContainerExists(ctx context.Context, name string) (bool, error) {
	_, err := dockercmd.ContainerInspect(ctx, name)
	if err == nil {
		return true, nil
	}
	if dockercmd.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

func staleSessionNetworkOwner(info dockercmd.NetworkInspectResponse, now time.Time) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(info.Labels[model.NetworkLabelManaged]), "true") || len(info.Containers) > 0 {
		return "", false
	}
	owner := strings.TrimSpace(info.Labels[model.NetworkLabelContainer])
	if owner == "" || info.Name != sessionNetworkName(owner) || strings.TrimSpace(info.Labels[model.NetworkLabelProjectHash]) == "" {
		return "", false
	}
	if !sessionNetworkPastGracePeriod(info, now) {
		return "", false
	}
	return owner, true
}

func fillSessionNetwork(ctx context.Context, session *backend.Session) {
	if session == nil || strings.TrimSpace(session.Ref.Name) == "" {
		return
	}
	info, err := networkInspect(ctx, sessionNetworkName(session.Ref.Name))
	if err != nil {
		return
	}
	labels := info.Labels
	if !strings.EqualFold(strings.TrimSpace(labels[model.NetworkLabelManaged]), "true") ||
		strings.TrimSpace(labels[model.NetworkLabelContainer]) != strings.TrimSpace(session.Ref.Name) ||
		strings.TrimSpace(labels[model.NetworkLabelProjectHash]) != strings.TrimSpace(session.ProjectHash) {
		return
	}
	session.Network = sessionNetworkFromInspect(info)
}

func fillSessionNetworks(ctx context.Context, sessions []backend.Session) {
	if len(sessions) == 0 {
		return
	}
	filters := dockercmd.NewFilters()
	filters.Add("label", model.NetworkLabelManaged+"=true")
	networks, err := networkList(ctx, filters)
	if err != nil {
		return
	}
	byOwner := make(map[string]dockercmd.NetworkInspectResponse, len(networks))
	for _, info := range networks {
		owner := strings.TrimSpace(info.Labels[model.NetworkLabelContainer])
		if !strings.EqualFold(strings.TrimSpace(info.Labels[model.NetworkLabelManaged]), "true") ||
			owner == "" || info.Name != sessionNetworkName(owner) {
			continue
		}
		byOwner[owner] = info
	}
	for i := range sessions {
		info, ok := byOwner[sessions[i].Ref.Name]
		if !ok {
			continue
		}
		if strings.TrimSpace(info.Labels[model.NetworkLabelProjectHash]) != strings.TrimSpace(sessions[i].ProjectHash) {
			continue
		}
		sessions[i].Network = sessionNetworkFromInspect(info)
	}
}

func sessionNetworkFromInspect(info dockercmd.NetworkInspectResponse) *backend.SessionNetwork {
	subnet := ""
	for _, cfg := range info.IPAM.Config {
		candidate := strings.TrimSpace(cfg.Subnet)
		if candidate != "" && !strings.Contains(candidate, ":") {
			subnet = candidate
			break
		}
	}
	return &backend.SessionNetwork{Name: info.Name, Subnet: subnet}
}
