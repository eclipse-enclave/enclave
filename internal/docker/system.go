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

// Ping verifies the container engine is usable. Docker needs a reachable
// daemon; podman is daemonless, so its client version suffices.
func Ping(ctx context.Context) error {
	if IsPodman() {
		_, err := capture(ctx, "version", "--format", "{{.Client.Version}}")
		return err
	}
	_, err := capture(ctx, "version", "--format", "{{.Server.Version}}")
	return err
}

// Info returns the subset of `docker info` we consume. Podman's info schema
// is entirely different; it is decoded separately and mapped onto the same
// SystemInfo shape (including a synthesized "name=rootless" security option)
// so consumers stay engine-neutral.
func Info(ctx context.Context) (SystemInfo, error) {
	out, err := capture(ctx, "info", "--format", "{{json .}}")
	if err != nil {
		return SystemInfo{}, err
	}
	line := firstLine(out)
	if line == "" {
		return SystemInfo{}, fmt.Errorf("%s info returned no output", engineName)
	}
	if IsPodman() {
		var info podmanInfo
		if err := json.Unmarshal([]byte(line), &info); err != nil {
			return SystemInfo{}, fmt.Errorf("decode podman info: %w", err)
		}
		return info.toSystemInfo(), nil
	}
	var info SystemInfo
	if err := json.Unmarshal([]byte(line), &info); err != nil {
		return SystemInfo{}, fmt.Errorf("decode docker info: %w", err)
	}
	return info, nil
}

// podmanInfo is the subset of `podman info` output we consume.
type podmanInfo struct {
	Host struct {
		Security struct {
			Rootless bool `json:"rootless"`
		} `json:"security"`
	} `json:"host"`
	Store struct {
		GraphRoot string `json:"graphRoot"`
	} `json:"store"`
}

func (p podmanInfo) toSystemInfo() SystemInfo {
	info := SystemInfo{DockerRootDir: p.Store.GraphRoot}
	if p.Host.Security.Rootless {
		info.SecurityOptions = append(info.SecurityOptions, "name=rootless")
	}
	return info
}

var (
	cachedInfoMu     sync.Mutex
	cachedInfoLoaded bool
	cachedInfo       SystemInfo
	cachedInfoErr    error
)

// CachedInfo returns Info, querying the engine at most once per process. Both
// rootless detection and the hardening warning consult it on every run, and
// `docker info` is a round trip to the daemon.
func CachedInfo(ctx context.Context) (SystemInfo, error) {
	cachedInfoMu.Lock()
	defer cachedInfoMu.Unlock()
	if !cachedInfoLoaded {
		cachedInfo, cachedInfoErr = Info(ctx)
		cachedInfoLoaded = true
	}
	return cachedInfo, cachedInfoErr
}

// resetCachedInfo clears the memoized info result; SetEngine calls it so an
// engine selected after an early query cannot observe stale data.
func resetCachedInfo() {
	cachedInfoMu.Lock()
	defer cachedInfoMu.Unlock()
	cachedInfoLoaded = false
	cachedInfo = SystemInfo{}
	cachedInfoErr = nil
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
