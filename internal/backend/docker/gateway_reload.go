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
	"sort"
	"strings"
	"time"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/gateway"
	"enclave/internal/model"
)

var (
	// Keep this conservative for slower hosts/CI while still failing quickly enough for CLI UX.
	gatewayReloadConfirmTimeout = 15 * time.Second
	gatewayReloadPollInterval   = 250 * time.Millisecond
)

var gatewayContainerInspect = dockercmd.ContainerInspect
var gatewayContainerLogsSince = dockercmd.ContainerLogsSince

// ListGateways lists running gateway sidecars, most-specific labels first.
func (b *Backend) ListGateways(ctx context.Context, filter backend.GatewayFilter) ([]backend.GatewayInfo, error) {
	filters := dockercmd.NewFilters()
	filters.Add("label", model.GatewayLabelManaged+"=true")
	if filter.Tool != "" {
		filters.Add("label", model.GatewayLabelAgent+"="+filter.Tool)
	}
	if filter.ProjectHash != "" {
		filters.Add("label", model.GatewayLabelProjectHash+"="+filter.ProjectHash)
	}
	if filter.WorkspaceID != "" {
		filters.Add("label", model.GatewayLabelWorkspaceHash+"="+filter.WorkspaceID)
	}

	containers, err := dockercmd.ContainerList(ctx, dockercmd.ListOptions{Filters: filters})
	if err != nil {
		return nil, err
	}
	return gatewayInfosFromSummaries(containers), nil
}

func gatewayInfosFromSummaries(containers []dockercmd.Summary) []backend.GatewayInfo {
	gateways := make([]backend.GatewayInfo, 0, len(containers))
	for _, c := range containers {
		gateways = append(gateways, backend.GatewayInfo{
			ID:               c.ID,
			Name:             containerDisplayName(c.Names, c.ID),
			Tool:             c.Labels[model.GatewayLabelAgent],
			ProjectHash:      c.Labels[model.GatewayLabelProjectHash],
			ProjectDir:       c.Labels[model.GatewayLabelProjectDir],
			WorkspaceID:      c.Labels[model.GatewayLabelWorkspaceHash],
			SessionContainer: c.Labels[model.GatewayLabelContainer],
		})
	}

	sort.Slice(gateways, func(i int, j int) bool {
		if gateways[i].ProjectHash != gateways[j].ProjectHash {
			return gateways[i].ProjectHash < gateways[j].ProjectHash
		}
		if gateways[i].Tool != gateways[j].Tool {
			return gateways[i].Tool < gateways[j].Tool
		}
		if gateways[i].Name != gateways[j].Name {
			return gateways[i].Name < gateways[j].Name
		}
		return gateways[i].ID < gateways[j].ID
	})

	return gateways
}

func containerDisplayName(names []string, containerID string) string {
	normalized := make([]string, 0, len(names))
	for _, name := range names {
		clean := strings.TrimPrefix(strings.TrimSpace(name), "/")
		if clean != "" {
			normalized = append(normalized, clean)
		}
	}
	sort.Strings(normalized)
	if len(normalized) > 0 {
		return normalized[0]
	}
	if len(containerID) <= 12 {
		return containerID
	}
	return containerID[:12]
}

// VerifyGatewayConfigMount checks that the gateway's config directory is
// bind-mounted from the expected host bundle directory.
func (b *Backend) VerifyGatewayConfigMount(ctx context.Context, id string, expectedSourceDir string) error {
	inspect, err := gatewayContainerInspect(ctx, id)
	if err != nil {
		return fmt.Errorf("inspect gateway container: %w", err)
	}

	expectedClean := filepath.Clean(expectedSourceDir)
	foundMount := false
	foundExpectedSource := false
	for _, mountPoint := range inspect.Mounts {
		if strings.TrimSpace(mountPoint.Destination) != model.GatewayConfigDir {
			continue
		}
		foundMount = true
		if filepath.Clean(mountPoint.Source) == expectedClean {
			foundExpectedSource = true
			break
		}
	}
	if !foundMount {
		return fmt.Errorf("gateway config mount (%s) missing; restart this session with the latest enclave", model.GatewayConfigDir)
	}
	if !foundExpectedSource {
		return fmt.Errorf("gateway config mount source mismatch (expected %s)", expectedClean)
	}
	return nil
}

// ReloadGatewayNetwork signals the gateway to reload its config bundle and
// waits for its logs to confirm the given generation.
func (b *Backend) ReloadGatewayNetwork(ctx context.Context, id string, generation string) error {
	signalAt := time.Now().UTC()
	if err := dockercmd.ContainerSignal(ctx, id, "SIGHUP"); err != nil {
		return err
	}
	return waitForGatewayReload(ctx, id, generation, signalAt)
}

func waitForGatewayReload(ctx context.Context, containerID string, generation string, since time.Time) error {
	successMarker := fmt.Sprintf("Reload completed (generation=%s)", generation)
	failureMarkers := []string{
		fmt.Sprintf("Reload validation failed (generation=%s)", generation),
		fmt.Sprintf("Reload apply failed (generation=%s)", generation),
		fmt.Sprintf("Reload ipset swap failed (generation=%s)", generation),
		fmt.Sprintf("Reload process restart failed (generation=%s)", generation),
	}
	deadline := time.Now().Add(gatewayReloadConfirmTimeout)
	for time.Now().Before(deadline) {
		logs, err := gatewayContainerLogsSince(ctx, containerID, since)
		if err != nil {
			return fmt.Errorf("read gateway logs after reload signal: %w", err)
		}
		if gateway.HasLogLine(logs, successMarker) {
			return nil
		}
		for _, marker := range failureMarkers {
			if gateway.HasLogLine(logs, marker) {
				return fmt.Errorf("gateway reload failed (%s)", marker)
			}
		}

		time.Sleep(gatewayReloadPollInterval)
	}

	inspect, err := gatewayContainerInspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("inspect gateway after reload wait timeout: %w", err)
	}
	if inspect.State == nil || !inspect.State.Running {
		return fmt.Errorf("gateway exited during reload")
	}
	return fmt.Errorf("timed out waiting for gateway reload confirmation (generation=%s)", generation)
}
