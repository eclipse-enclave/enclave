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
)

// Ping verifies the Docker daemon is reachable.
func Ping(ctx context.Context) error {
	_, err := capture(ctx, "version", "--format", "{{.Server.Version}}")
	return err
}

// Info returns the subset of `docker info` we consume. Under podman the
// equivalent fields are read from its own `info` schema and mapped onto the
// Docker names (storage root; rootless mode reported as a security option).
// ServerVersion, OSType, Warnings, and FirewallBackend stay empty under
// podman: they describe the Docker daemon, and callers that branch on the
// Docker version or firewall backend must gate on IsPodman rather than rely on
// the empty values.
func Info(ctx context.Context) (SystemInfo, error) {
	out, err := capture(ctx, "info", "--format", "{{json .}}")
	if err != nil {
		return SystemInfo{}, err
	}
	line := firstLine(out)
	if line == "" {
		return SystemInfo{}, fmt.Errorf("%s info returned no output", Binary())
	}
	if IsPodman() {
		return decodePodmanInfo([]byte(line))
	}
	var info SystemInfo
	if err := json.Unmarshal([]byte(line), &info); err != nil {
		return SystemInfo{}, fmt.Errorf("decode docker info: %w", err)
	}
	return info, nil
}

func decodePodmanInfo(data []byte) (SystemInfo, error) {
	var raw struct {
		Host struct {
			Security struct {
				Rootless bool `json:"rootless"`
			} `json:"security"`
			NetworkBackend     string `json:"networkBackend"`
			NetworkBackendInfo struct {
				Backend string `json:"backend"`
				Version string `json:"version"`
			} `json:"networkBackendInfo"`
		} `json:"host"`
		Store struct {
			GraphRoot string `json:"graphRoot"`
		} `json:"store"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return SystemInfo{}, fmt.Errorf("decode podman info: %w", err)
	}
	info := SystemInfo{
		DockerRootDir:         raw.Store.GraphRoot,
		NetworkBackend:        raw.Host.NetworkBackend,
		NetworkBackendVersion: raw.Host.NetworkBackendInfo.Version,
	}
	if info.NetworkBackend == "" {
		info.NetworkBackend = raw.Host.NetworkBackendInfo.Backend
	}
	if raw.Host.Security.Rootless {
		info.SecurityOptions = []string{"name=rootless"}
	}
	return info, nil
}
