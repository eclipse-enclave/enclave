// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"sort"
	"strings"

	"enclave/internal/config"
	"enclave/internal/model"
)

// resolveFeatureSelection returns the feature names build selects and the
// subset of them that matches no available feature.
func resolveFeatureSelection(paths model.Paths, build model.BuildOptions) (selected []string, unknown []string, err error) {
	if featuresDisabled(build) {
		return []string{}, nil, nil
	}
	allFeatures, err := config.ListFeatures(paths)
	if err != nil {
		return nil, nil, err
	}
	selected = selectFeatures(allFeatures, build)
	available := make(map[string]struct{}, len(allFeatures))
	for _, feature := range allFeatures {
		available[feature.Name] = struct{}{}
	}
	for _, name := range selected {
		if _, ok := available[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	return selected, unknown, nil
}

func selectFeatures(allFeatures []model.Extension, build model.BuildOptions) []string {
	switch {
	case featuresDisabled(build):
		return []string{}
	case build.Features == nil:
		return defaultEnabledFeatureNames(allFeatures)
	default:
		return resolveConfiguredFeatures(build.Features, allFeatures)
	}
}

func featuresDisabled(build model.BuildOptions) bool {
	return build.Slim || (build.Devcontainer && build.Features == nil)
}

func resolveConfiguredFeatures(requested []string, allFeatures []model.Extension) []string {
	if requested == nil {
		return nil
	}
	normalized := normalizeAndSortNames(requested)
	if len(normalized) == 0 {
		return []string{}
	}
	if len(normalized) == 1 {
		switch strings.ToLower(strings.TrimSpace(normalized[0])) {
		case model.SelectionDefault:
			return defaultEnabledFeatureNames(allFeatures)
		case model.FeatureSelectionAll:
			return featureNameList(allFeatures)
		}
	}

	// Expand keyword selectors ("default", "all") and additive directives
	// (+feature, -feature) against the available feature set.
	if hasFeatureDirectives(normalized) || hasFeatureSelectors(normalized) {
		return expandFeatureDirectives(normalized, allFeatures)
	}
	return normalized
}

// expandFeatureDirectives resolves a mixed list of selectors ("default",
// "all"), additive directives (+/-), and literal feature names into a flat
// sorted list of feature names.
//
// Processing uses two passes so that removals always apply after additions
// regardless of list order (normalizeAndSortNames sorts alphabetically).
func expandFeatureDirectives(normalized []string, allFeatures []model.Extension) []string {
	selected := make(map[string]struct{}, len(allFeatures))

	// If the list contains only directives (+/-) with no selector keyword,
	// seed from default-enabled features so the directives have a base.
	if !hasFeatureSelectors(normalized) {
		for _, f := range allFeatures {
			if f.DefaultEnabled {
				selected[f.Name] = struct{}{}
			}
		}
	}

	// Pass 1: additions — expand selectors and collect explicit names.
	for _, raw := range normalized {
		value := strings.TrimSpace(raw)
		if value == "" || strings.HasPrefix(value, "-") {
			continue
		}

		bare := value
		if strings.HasPrefix(value, "+") {
			bare = strings.TrimSpace(value[1:])
			if bare == "" {
				continue
			}
		}

		switch strings.ToLower(bare) {
		case model.SelectionDefault:
			for _, f := range allFeatures {
				if f.DefaultEnabled {
					selected[f.Name] = struct{}{}
				}
			}
		case model.FeatureSelectionAll:
			for _, f := range allFeatures {
				selected[f.Name] = struct{}{}
			}
		default:
			selected[bare] = struct{}{}
		}
	}

	// Pass 2: removals.
	for _, raw := range normalized {
		value := strings.TrimSpace(raw)
		if !strings.HasPrefix(value, "-") {
			continue
		}
		feature := strings.TrimSpace(value[1:])
		if feature != "" {
			delete(selected, feature)
		}
	}

	resolved := make([]string, 0, len(selected))
	for feature := range selected {
		resolved = append(resolved, feature)
	}
	sort.Strings(resolved)
	return resolved
}

func featureNameList(features []model.Extension) []string {
	names := make([]string, 0, len(features))
	for _, feature := range features {
		names = append(names, feature.Name)
	}
	return names
}

func defaultEnabledFeatureNames(features []model.Extension) []string {
	names := make([]string, 0, len(features))
	for _, feature := range features {
		if feature.DefaultEnabled {
			names = append(names, feature.Name)
		}
	}
	return names
}

func hasFeatureSelectors(values []string) bool {
	for _, raw := range values {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case model.SelectionDefault, model.FeatureSelectionAll:
			return true
		}
	}
	return false
}

func hasFeatureDirectives(values []string) bool {
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
			return true
		}
	}
	return false
}
