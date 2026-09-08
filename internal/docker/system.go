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
		} `json:"host"`
		Store struct {
			GraphRoot string `json:"graphRoot"`
		} `json:"store"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return SystemInfo{}, fmt.Errorf("decode podman info: %w", err)
	}
	info := SystemInfo{DockerRootDir: raw.Store.GraphRoot}
	if raw.Host.Security.Rootless {
		info.SecurityOptions = []string{"name=rootless"}
	}
	return info, nil
}
