// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/config"
	"enclave/internal/model"
)

func (r *Runtime) addCacheMounts(mounts *mountAccumulator) error {
	pnpmStoreDir := r.containerHome + "/.local/share/pnpm/store"
	mounts.AddEnv("PNPM_CONFIG_STORE_DIR", pnpmStoreDir)

	if r.run.NoCache {
		return nil
	}
	caches, err := r.projectCaches()
	if err != nil {
		return err
	}
	cacheDir := config.HostCacheToolProjectDir(r.host.Home, r.profile.Name, r.project.Hash)
	for _, cache := range caches {
		hostDir := filepath.Join(cacheDir, cache.Name)
		_ = os.MkdirAll(hostDir, 0o700)
		mounts.AddMount(bindMount(hostDir, r.containerHome+"/"+cache.Target, false))
	}
	return nil
}

// cacheClaim records one home-relative target already taken, either by a cache
// (name set) or by another session mount such as the tool config store.
type cacheClaim struct {
	owner  string
	name   string
	target string
}

// projectCaches returns the built-in package caches plus those the tool spec
// and enabled features declare. Per-spec validation happens at load time; the
// cross-extension rules apply here, where profile and features meet: an
// identical name+target pair dedupes, while a name claimed with a different
// target or a target overlapping any prior claim (including the tool's own
// config and memory store mounts) is an error naming both claimants.
func (r *Runtime) projectCaches() ([]model.CacheConfig, error) {
	caches := append([]model.CacheConfig(nil), config.BuiltinProjectCaches...)
	claims := r.profileMountClaims()
	for _, cache := range config.BuiltinProjectCaches {
		claims = append(claims, cacheClaim{owner: "the built-in cache list", name: cache.Name, target: cache.Target})
	}
	add := func(owner string, cache model.CacheConfig) error {
		for _, c := range claims {
			if c.name == cache.Name {
				if c.target == cache.Target {
					return nil
				}
				return fmt.Errorf("cache %q: %s declares target %q but %s declares target %q",
					cache.Name, owner, cache.Target, c.owner, c.target)
			}
			if config.CacheTargetsOverlap(c.target, cache.Target) {
				return fmt.Errorf("cache %q (%s): target %q overlaps %q, claimed by %s",
					cache.Name, owner, cache.Target, c.target, c.owner)
			}
		}
		claims = append(claims, cacheClaim{owner: owner, name: cache.Name, target: cache.Target})
		caches = append(caches, cache)
		return nil
	}
	for _, cache := range r.profile.Caches {
		if err := add(fmt.Sprintf("tool %q", r.profile.Name), cache); err != nil {
			return nil, err
		}
	}
	for _, feature := range r.features {
		for _, cache := range feature.Caches {
			if err := add(fmt.Sprintf("feature %q", feature.Name), cache); err != nil {
				return nil, err
			}
		}
	}
	return caches, nil
}

// profileMountClaims returns the home-relative targets of the tool's own
// config and memory store mounts, so a cache colliding with them fails here
// instead of at container start. Paths resolving outside the container home
// cannot overlap a cache target and are skipped.
func (r *Runtime) profileMountClaims() []cacheClaim {
	var claims []cacheClaim
	add := func(owner string, path string) {
		if strings.TrimSpace(path) == "" {
			return
		}
		resolved := resolveContainerProfilePath(r.containerHome, path)
		if rel, ok := strings.CutPrefix(resolved, r.containerHome+"/"); ok && rel != "" {
			claims = append(claims, cacheClaim{owner: owner, target: rel})
		}
	}
	add("the tool config store mount", r.profile.ConfigDir)
	add("the tool memory mount", r.profile.MemoryDir)
	return claims
}
