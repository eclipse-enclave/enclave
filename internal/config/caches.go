// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"

	"enclave/internal/model"
	"enclave/internal/util"
)

// BuiltinProjectCaches are the package caches every session mounts unless
// --no-cache. Extension-declared caches must not collide with them by name or
// target.
var BuiltinProjectCaches = []model.CacheConfig{
	{Name: "npm", Target: ".npm"},
	{Name: "pip", Target: ".cache/pip"},
	{Name: "go", Target: "go/pkg/mod"},
	{Name: "go-build", Target: ".cache/go-build"},
	{Name: "cargo", Target: ".cargo"},
	{Name: "pnpm", Target: ".local/share/pnpm"},
	{Name: "uv", Target: ".cache/uv"},
	{Name: "yarn", Target: ".cache/yarn"},
	{Name: "bun", Target: ".bun"},
	{Name: "nvm", Target: ".nvm/versions"},
}

// CacheTargetsOverlap reports whether two home-relative mount targets collide:
// equal or nested either way. Nested bind mounts depend on mount order and
// silently shadow each other, so overlap counts as a collision.
func CacheTargetsOverlap(a string, b string) bool {
	return util.PathWithin(a, b) || util.PathWithin(b, a)
}

// reservedCacheTargets maps each container-home-relative path the runtime
// already claims (built-in caches, auth stores, shell history, the SSH mount,
// persistent config files) to a label for collision error messages.
func reservedCacheTargets() map[string]string {
	reserved := map[string]string{
		model.ContainerAuthDir:        "the auth store",
		model.ContainerFeatureAuthDir: "the feature auth store",
		model.ContainerHistoryDir:     "the shell history store",
		model.ContainerSSHDir:         "the SSH mount",
	}
	for _, file := range model.ContainerHomeConfigFiles {
		reserved[file] = "a persistent config file mount"
	}
	for _, cache := range BuiltinProjectCaches {
		reserved[cache.Target] = fmt.Sprintf("the built-in %q cache", cache.Name)
	}
	return reserved
}

// validateAndNormalizeCaches checks a spec's declared cache mounts: names are
// held to the extension-name charset (they become host directories), targets
// must stay inside the container home, and overlaps with built-in caches or
// reserved targets are load errors. An identical name+target pair dedupes; a
// name redeclared with a different target or a target overlapping another
// entry is an error. Cross-extension collisions are checked at session start
// (runtime's projectCaches).
func validateAndNormalizeCaches(caches []model.CacheConfig) ([]model.CacheConfig, error) {
	if len(caches) == 0 {
		return nil, nil
	}
	builtinNames := map[string]struct{}{}
	for _, cache := range BuiltinProjectCaches {
		builtinNames[cache.Name] = struct{}{}
	}
	reservedTargets := reservedCacheTargets()

	targetByName := map[string]string{}
	out := make([]model.CacheConfig, 0, len(caches))
	for i, cache := range caches {
		if cache.Name == "" {
			return nil, fmt.Errorf("caches[%d]: name must not be empty", i)
		}
		if !extensionNamePattern.MatchString(cache.Name) {
			return nil, fmt.Errorf("caches[%d]: invalid name %q: %s", i, cache.Name, ExtensionNameCharset)
		}
		target, err := cleanHomeRelativePath(cache.Target)
		if err != nil {
			return nil, fmt.Errorf("caches[%d] (%q): target: %w", i, cache.Name, err)
		}
		if _, ok := builtinNames[cache.Name]; ok {
			return nil, fmt.Errorf("caches[%d]: name %q is reserved by a built-in cache", i, cache.Name)
		}
		for reservedTarget, owner := range reservedTargets {
			if CacheTargetsOverlap(target, reservedTarget) {
				return nil, fmt.Errorf("caches[%d] (%q): target %q overlaps %q, reserved by %s", i, cache.Name, target, reservedTarget, owner)
			}
		}
		if seen, ok := targetByName[cache.Name]; ok {
			if seen == target {
				continue
			}
			return nil, fmt.Errorf("caches[%d]: name %q declared twice with different targets (%q and %q)", i, cache.Name, seen, target)
		}
		for _, accepted := range out {
			if CacheTargetsOverlap(accepted.Target, target) {
				return nil, fmt.Errorf("caches[%d] (%q): target %q overlaps target %q of cache %q", i, cache.Name, target, accepted.Target, accepted.Name)
			}
		}
		targetByName[cache.Name] = target
		out = append(out, model.CacheConfig{Name: cache.Name, Target: target})
	}
	return out, nil
}
