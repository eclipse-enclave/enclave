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

// Info returns the subset of `docker info` we consume.
func Info(ctx context.Context) (SystemInfo, error) {
	out, err := capture(ctx, "info", "--format", "{{json .}}")
	if err != nil {
		return SystemInfo{}, err
	}
	line := firstLine(out)
	if line == "" {
		return SystemInfo{}, fmt.Errorf("docker info returned no output")
	}
	var info SystemInfo
	if err := json.Unmarshal([]byte(line), &info); err != nil {
		return SystemInfo{}, fmt.Errorf("decode docker info: %w", err)
	}
	return info, nil
}
