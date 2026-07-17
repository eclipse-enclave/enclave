// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"path"
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/model"
)

// hostConfigPassthroughBackstop is the non-configurable deny-list that still
// applies even when a user widens the reviewed passthrough allow-list.
var hostConfigPassthroughBackstop = []string{
	"history.jsonl",
	"*.history",
	"projects/",
	"sessions/",
	"agent/sessions/",
	"session/",
	"statsig/",
	"todos/",
	"telemetry/",
	"cache/",
	".cache/",
	"logs/",
	"tmp/",
	".tmp/",
}

func HostConfigPassthroughDefaults(profile model.Profile) []string {
	return subtractDeniedHostConfigPaths(profile, profile.PassthroughPaths)
}

func HostConfigPassthroughDeniedPaths(profile model.Profile) []string {
	denied := make([]string, 0, len(profile.RuntimeAuthFiles())+len(hostConfigPassthroughBackstop)+1)
	denied = append(denied, profile.RuntimeAuthFiles()...)
	if credentials := normalizeHostConfigPath(profile.HostCredentialsFile); credentials != "" {
		denied = append(denied, credentials)
	}
	denied = append(denied, hostConfigPassthroughBackstop...)
	return normalizeHostConfigPathList(denied)
}

// HostConfigPassthroughDeniedAbsolutePaths returns absolute host paths of the
// auth/OAuth files that must never be passed through, including files that live
// outside the tool config dir (such as Claude's ~/.claude.json). The relative
// deny-list cannot express those, so the overlay rejects any passthrough symlink
// whose resolved target matches one of them.
func HostConfigPassthroughDeniedAbsolutePaths(home string, profile model.Profile) []string {
	var paths []string
	if configDir := HostProfileConfigDir(home, profile); configDir != "" {
		for _, rel := range profile.ProviderAuthFiles() {
			if rel = strings.TrimSpace(rel); rel == "" {
				continue
			}
			paths = append(paths, filepath.Join(configDir, filepath.FromSlash(rel)))
		}
	}
	if credentials := HostProfileCredentialsPath(home, profile); credentials != "" {
		paths = append(paths, credentials)
	}
	if oauth := HostProfileOAuthJSONPath(home, profile); oauth != "" {
		paths = append(paths, oauth)
	}
	return paths
}

func ResolveHostConfigPaths(profile model.Profile, configured []string) []string {
	if configured == nil {
		return HostConfigPassthroughDefaults(profile)
	}
	if len(configured) == 0 {
		return []string{}
	}

	normalizedDefaults := normalizeHostConfigPathList(profile.PassthroughPaths)
	normalizedConfigured := normalizeHostConfigPathList(configured)
	if len(normalizedConfigured) == 0 {
		return []string{}
	}

	selected := make(map[string]struct{}, len(normalizedDefaults)+len(normalizedConfigured))
	if shouldSeedHostConfigPaths(normalizedConfigured) {
		for _, value := range normalizedDefaults {
			selected[value] = struct{}{}
		}
	}

	for _, raw := range normalizedConfigured {
		if strings.HasPrefix(raw, "-") {
			continue
		}

		bare := strings.TrimPrefix(raw, "+")
		switch strings.ToLower(bare) {
		case model.SelectionDefault:
			for _, value := range normalizedDefaults {
				selected[value] = struct{}{}
			}
		default:
			selected[bare] = struct{}{}
		}
	}

	for _, raw := range normalizedConfigured {
		if !strings.HasPrefix(raw, "-") {
			continue
		}

		bare := strings.TrimSpace(strings.TrimPrefix(raw, "-"))
		switch strings.ToLower(bare) {
		case "", model.SelectionDefault:
			continue
		default:
			delete(selected, bare)
		}
	}

	resolved := make([]string, 0, len(selected))
	for value := range selected {
		resolved = append(resolved, value)
	}
	sort.Strings(resolved)
	return subtractDeniedHostConfigPaths(profile, resolved)
}

func shouldSeedHostConfigPaths(configured []string) bool {
	for _, raw := range configured {
		if raw == "" {
			continue
		}
		if strings.EqualFold(raw, model.SelectionDefault) {
			return false
		}
		if !strings.HasPrefix(raw, "+") && !strings.HasPrefix(raw, "-") {
			return false
		}
	}
	return true
}

func normalizeHostConfigPathList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, raw := range values {
		value := normalizeHostConfigPath(raw)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized
}

func normalizeHostConfigPath(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}

	prefix := ""
	if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		prefix = value[:1]
		value = strings.TrimSpace(value[1:])
	}
	if value == "" {
		return ""
	}
	if strings.EqualFold(value, model.SelectionDefault) {
		return prefix + model.SelectionDefault
	}

	isDir := strings.HasSuffix(value, "/")
	value = strings.TrimPrefix(strings.TrimPrefix(filepathToSlash(value), "./"), "/")
	value = path.Clean(value)
	if value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return ""
	}
	if isDir && !strings.HasSuffix(value, "/") {
		value += "/"
	}
	return prefix + value
}

func subtractDeniedHostConfigPaths(profile model.Profile, values []string) []string {
	normalizedDenied := HostConfigPassthroughDeniedPaths(profile)

	filtered := make([]string, 0, len(values))
	for _, value := range normalizeHostConfigPathList(values) {
		if isDeniedHostConfigPath(value, normalizedDenied) {
			continue
		}
		filtered = append(filtered, value)
	}
	if filtered == nil {
		return []string{}
	}
	return filtered
}

func isDeniedHostConfigPath(relativePath string, denied []string) bool {
	for _, deny := range denied {
		if HostConfigPathMatches(relativePath, deny) {
			return true
		}
	}
	return false
}

// HostConfigPathMatches reports whether a normalized relative config path
// matches a host-config allow/deny pattern.
//
// Matching rules:
// - trailing "/" means directory prefix match
// - glob patterns are matched against both the full relative path and basename
// - otherwise the match is exact
func HostConfigPathMatches(relativePath string, pattern string) bool {
	if relativePath == "" || pattern == "" {
		return false
	}

	if strings.HasSuffix(pattern, "/") {
		dirPath := strings.TrimSuffix(pattern, "/")
		return relativePath == dirPath || strings.HasPrefix(relativePath, pattern)
	}

	if strings.ContainsAny(pattern, "*?[") {
		if ok, _ := path.Match(pattern, relativePath); ok {
			return true
		}
		if ok, _ := path.Match(pattern, path.Base(relativePath)); ok {
			return true
		}
		return false
	}

	return relativePath == pattern
}

func filepathToSlash(value string) string {
	return strings.ReplaceAll(value, "\\", "/")
}
