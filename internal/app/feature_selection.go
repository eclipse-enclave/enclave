// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"sort"
	"strings"

	"enclave/internal/config"
	"enclave/internal/model"
)

func validateFeatureNames(requested []string, allFeatures []model.Extension) ([]string, error) {
	available := make(map[string]struct{}, len(allFeatures))
	for _, feature := range allFeatures {
		available[feature.Name] = struct{}{}
	}

	var unknown, unknownRemovals []string
	additions, removals := config.SplitFeatureDirectives(requested)
	for _, name := range additions {
		if _, ok := available[name]; !ok && !config.IsFeatureSelector(name) {
			unknown = append(unknown, name)
		}
	}
	for _, name := range removals {
		if _, ok := available[name]; !ok {
			unknownRemovals = append(unknownRemovals, name)
		}
	}

	var warnings []string
	if len(unknownRemovals) > 0 {
		warnings = append(warnings, fmt.Sprintf("ignoring removal of unknown features: %s; run 'enclave features list' to see available features", strings.Join(normalizeAndSortNames(unknownRemovals), ", ")))
	}
	if len(unknown) > 0 {
		return warnings, fmt.Errorf("unknown features: %s; run 'enclave features list' to see available features", strings.Join(normalizeAndSortNames(unknown), ", "))
	}
	return warnings, nil
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
	if config.HasFeatureDirectives(normalized) || config.HasFeatureSelectors(normalized) {
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
	additions, removals := config.SplitFeatureDirectives(normalized)

	// If the list contains only directives (+/-) with no selector keyword,
	// seed from default-enabled features so the directives have a base.
	if !config.HasFeatureSelectors(normalized) {
		for _, f := range allFeatures {
			if f.DefaultEnabled {
				selected[f.Name] = struct{}{}
			}
		}
	}

	// Pass 1: additions — expand selectors and collect explicit names.
	for _, name := range additions {
		switch strings.ToLower(name) {
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
			selected[name] = struct{}{}
		}
	}

	// Pass 2: removals.
	for _, name := range removals {
		delete(selected, name)
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
