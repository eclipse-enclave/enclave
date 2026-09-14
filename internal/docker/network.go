// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// NetworkCreate creates a Docker network and returns its ID.
func NetworkCreate(ctx context.Context, opts NetworkCreateOptions) (string, error) {
	args := []string{"network", "create"}
	if driver := strings.TrimSpace(opts.Driver); driver != "" {
		args = append(args, "--driver", driver)
	}
	if opts.EnableIPv6 != nil {
		args = append(args, fmt.Sprintf("--ipv6=%t", *opts.EnableIPv6))
	}
	if subnet := strings.TrimSpace(opts.Subnet); subnet != "" {
		args = append(args, "--subnet", subnet)
	}
	args = append(args, sortedMapFlags("--opt", opts.Options)...)
	args = append(args, sortedMapFlags("--label", opts.Labels)...)
	args = append(args, opts.Name)
	out, err := capture(ctx, args...)
	if err != nil {
		return out, err
	}
	if !IsPodman() {
		return out, nil
	}
	// podman echoes the network's name where Docker prints its ID. Callers
	// identify a network by the ID they get back and compare it against
	// inspect output, so the name has to be resolved here rather than silently
	// standing in for an ID.
	info, err := NetworkInspect(ctx, opts.Name)
	if err != nil {
		return "", fmt.Errorf("resolve ID of created network %q: %w", opts.Name, err)
	}
	if id := strings.TrimSpace(info.ID); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("created network %q reports no ID", opts.Name)
}

// NetworkRemove removes a Docker network.
func NetworkRemove(ctx context.Context, name string) error {
	_, err := capture(ctx, "network", "rm", name)
	return err
}

// NetworkAttachedContainers returns the names of the containers the engine
// considers attached to a network, stopped ones included. It replaces the
// inspect response's endpoint map, which podman does not populate at all, and
// counts the same containers podman's own in-use check does.
func NetworkAttachedContainers(ctx context.Context, name string) ([]string, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("cannot list attached containers without a network name")
	}
	out, err := capture(ctx, "ps", "--all", "--no-trunc", "--filter", "network="+name, "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// NetworkInspect returns the inspect view of a single Docker network.
func NetworkInspect(ctx context.Context, name string) (NetworkInspectResponse, error) {
	inspected, err := inspectNetworks(ctx, []string{name})
	if err != nil {
		return NetworkInspectResponse{}, err
	}
	if len(inspected) == 0 {
		return NetworkInspectResponse{}, &cliError{
			args:   []string{"network", "inspect", name},
			stderr: "no such network: " + name,
			err:    fmt.Errorf("network not found"),
		}
	}
	return inspected[0], nil
}

// NetworkList returns inspect views for networks matching filters.
func NetworkList(ctx context.Context, filters Filters) ([]NetworkInspectResponse, error) {
	args := []string{"network", "ls", "--no-trunc", "--format", "{{.ID}}"}
	args = append(args, filters.flags()...)
	out, err := capture(ctx, args...)
	if err != nil {
		return nil, err
	}
	ids := splitLines(out)
	if len(ids) == 0 {
		return nil, nil
	}
	return inspectNetworks(ctx, ids)
}

func inspectNetworks(ctx context.Context, ids []string) ([]NetworkInspectResponse, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := append([]string{"network", "inspect", "--format", "{{json .}}"}, ids...)
	out, err := capture(ctx, args...)
	results := decodeNetworkInspectResponses(out)
	if err != nil && IsNotFound(err) {
		return results, nil
	}
	if len(results) == 0 && err != nil {
		return nil, err
	}
	return results, nil
}

func decodeNetworkInspectResponses(out string) []NetworkInspectResponse {
	lines := splitLines(out)
	results := make([]NetworkInspectResponse, 0, len(lines))
	for _, line := range lines {
		var info NetworkInspectResponse
		if err := json.Unmarshal([]byte(line), &info); err != nil {
			continue
		}
		results = append(results, info)
	}
	return results
}
