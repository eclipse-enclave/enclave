// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"maps"
	"slices"
	"strings"
)

// mergeStringMap returns base with override's keys set, leaving both inputs
// unchanged.
func mergeStringMap(base, override map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(override))
	maps.Copy(merged, base)
	maps.Copy(merged, override)
	return merged
}

// validateHostEnv drops host_env entries (top level and per tool override)
// that are not environment variable names or that change how the session's
// engine client reaches its engine.
func validateHostEnv(path string, defaults *Defaults) []Warning {
	var warnings []Warning
	drop := func(env map[string]string, tool string, label string) {
		for _, key := range slices.Sorted(maps.Keys(env)) {
			reason := ""
			switch {
			case !isValidEnvKey(key):
				reason = "not a valid environment variable name"
			case hostEnvChangesEngine(key):
				reason = "it would change which engine, engine configuration, or helper binaries the session's engine client uses"
			default:
				continue
			}
			delete(env, key)
			warnings = append(warnings, newWarning(WarningInvalidValue, "host_env", tool, path, "Ignoring %s entry %q in %s: %s", label, key, path, reason))
		}
	}
	drop(defaults.HostEnv, "", "host_env")
	for _, tool := range slices.Sorted(maps.Keys(defaults.ToolOverrides)) {
		drop(defaults.ToolOverrides[tool].HostEnv, tool, "tool_overrides."+tool+".host_env")
	}
	return warnings
}

// hostEnvChangesEngine is best effort: docker reads its context from
// $HOME/.docker and credential helpers from PATH; podman runs locally and
// reads XDG_* and CONTAINERS_* paths.
func hostEnvChangesEngine(key string) bool {
	if key == "HOME" || key == "PATH" {
		return true
	}
	for _, prefix := range []string{"DOCKER_", "CONTAINER_", "CONTAINERS_", "XDG_"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
