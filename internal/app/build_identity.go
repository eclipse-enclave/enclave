// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

// effectiveBuildIdentity returns the uid/gid the image bakes its agent user
// with. It defaults to the container-side host identity so the image's agent
// files are owned by the invoking host user: that is the host uid/gid on a
// rootful daemon, and 0:0 under rootless Docker, where the session sandbox then
// maps those ids to the agent's normal uid/gid.
func effectiveBuildIdentity(host model.Host, opts model.BuildOptions) (uid string, gid string) {
	containerUID, containerGID := host.ContainerIdentity()
	uid = strings.TrimSpace(opts.BuildUID)
	if uid == "" {
		uid = containerUID
	}
	gid = strings.TrimSpace(opts.BuildGID)
	if gid == "" {
		gid = containerGID
	}
	return uid, gid
}

func appendEffectiveBuildIdentityHashSuffix(suffix string, host model.Host, opts model.BuildOptions) string {
	// This runs after host resolution, so it captures the actual UID/GID baked
	// into the image even when --build-uid/--build-gid were not explicit.
	uid, gid := effectiveBuildIdentity(host, opts)
	if strings.TrimSpace(uid) != "" {
		suffix += "-effective-build-uid-" + util.HashString(uid)
	}
	if strings.TrimSpace(gid) != "" {
		suffix += "-effective-build-gid-" + util.HashString(gid)
	}
	return suffix
}

func resolveBuildxCacheFrom(opts model.BuildOptions) []string {
	values := cleanBuildxCacheSpecs(opts.BuildxCacheFrom)
	cacheDir := strings.TrimSpace(opts.BuildxCacheDir)
	if cacheDir != "" && buildxLocalCachePopulated(cacheDir) {
		values = append(values, "type=local,src="+cacheDir)
	}
	return values
}

func resolveBuildxCacheTo(opts model.BuildOptions) ([]string, error) {
	values := cleanBuildxCacheSpecs(opts.BuildxCacheTo)
	cacheDir := strings.TrimSpace(opts.BuildxCacheDir)
	if cacheDir != "" {
		if err := os.MkdirAll(cacheDir, 0o700); err != nil {
			return nil, err
		}
		values = append(values, "type=local,dest="+cacheDir+",mode=max")
	}
	return values, nil
}

func cleanBuildxCacheSpecs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	cleaned := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		cleaned = append(cleaned, value)
	}
	return util.Dedupe(cleaned)
}

func buildxLocalCachePopulated(cacheDir string) bool {
	if strings.TrimSpace(cacheDir) == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(cacheDir, "index.json"))
	return err == nil && !info.IsDir()
}
