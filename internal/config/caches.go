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
)

// reservedCacheTargets maps each container-home-relative path the runtime
// already claims to a label naming its owner, so a cache colliding with one is
// rejected with a message that says what it collides with. Besides the
// built-in caches these are the auth-store mounts and the shell-history mount
// (see runtime.addHistoryMounts).
func reservedCacheTargets() map[string]string {
	reserved := map[string]string{
		model.ContainerAuthDir:        "the auth store",
		model.ContainerFeatureAuthDir: "the feature auth store",
		model.ContainerHistoryDir:     "the shell history store",
	}
	for _, cache := range model.BuiltinProjectCaches {
		reserved[cache.Target] = fmt.Sprintf("the built-in %q cache", cache.Name)
	}
	return reserved
}

// validateAndNormalizeCaches checks a spec's declared cache mounts. Each name
// becomes a host directory under the enclave-managed project cache dir, so it
// is held to the extension-name charset; each target is a container path that
// must stay inside the container home. Collisions with built-in cache names
// and reserved targets are load errors. Within the list, a repeated name with
// the same target dedupes, while a name or target redeclared with a different
// counterpart is an error. Cross-extension collisions can only be checked at
// session start, where the tool spec and the enabled features meet.
func validateAndNormalizeCaches(caches []model.CacheConfig) ([]model.CacheConfig, error) {
	if len(caches) == 0 {
		return nil, nil
	}
	builtinNames := map[string]struct{}{}
	for _, cache := range model.BuiltinProjectCaches {
		builtinNames[cache.Name] = struct{}{}
	}
	reservedTargets := reservedCacheTargets()

	targetByName := map[string]string{}
	nameByTarget := map[string]string{}
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
		if owner, ok := reservedTargets[target]; ok {
			return nil, fmt.Errorf("caches[%d] (%q): target %q is reserved by %s", i, cache.Name, target, owner)
		}
		if seen, ok := targetByName[cache.Name]; ok {
			if seen == target {
				continue
			}
			return nil, fmt.Errorf("caches[%d]: name %q declared twice with different targets (%q and %q)", i, cache.Name, seen, target)
		}
		if seen, ok := nameByTarget[target]; ok {
			return nil, fmt.Errorf("caches[%d]: target %q declared twice (as %q and %q)", i, target, seen, cache.Name)
		}
		targetByName[cache.Name] = target
		nameByTarget[target] = cache.Name
		out = append(out, model.CacheConfig{Name: cache.Name, Target: target})
	}
	return out, nil
}
