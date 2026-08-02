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
	"sync"
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

var (
	cachedInfoOnce sync.Once
	cachedInfo     SystemInfo
	cachedInfoErr  error
)

// CachedInfo returns Info, querying the daemon at most once per process. Both
// rootless detection and the hardening warning consult it on every run, and
// `docker info` is a round trip to the daemon.
func CachedInfo(ctx context.Context) (SystemInfo, error) {
	cachedInfoOnce.Do(func() {
		cachedInfo, cachedInfoErr = Info(ctx)
	})
	return cachedInfo, cachedInfoErr
}

// HasSecurityOption reports whether `docker info` advertised the named security
// option, for example "rootless" or "userns".
func (i SystemInfo) HasSecurityOption(name string) bool {
	for _, option := range i.SecurityOptions {
		if strings.Contains(option, "name="+name) {
			return true
		}
	}
	return false
}

// IsRootless reports whether the daemon runs in rootless mode. A rootless
// daemon confines containers to a user namespace in which the invoking host
// user is mapped to container UID 0, so host-owned bind mounts appear
// root-owned inside containers.
func IsRootless(ctx context.Context) (bool, error) {
	info, err := CachedInfo(ctx)
	if err != nil {
		return false, err
	}
	return info.HasSecurityOption("rootless"), nil
}
