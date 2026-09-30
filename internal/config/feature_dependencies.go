// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"sort"

	"enclave/internal/model"
)

// ResolveFeatureDependencies expands a selection once, with dependencies before consumers.
func ResolveFeatureDependencies(features []model.Extension, selected []string, excluded map[string]bool) ([]model.Extension, error) {
	catalog := map[string]model.Extension{}
	for _, feature := range features {
		catalog[feature.Name] = feature
	}
	state := map[string]int{}
	result := []model.Extension{}
	var visit func(string) error
	visit = func(name string) error {
		if excluded[name] {
			return fmt.Errorf("required feature %q is explicitly excluded", name)
		}
		if state[name] == 1 {
			return fmt.Errorf("feature dependency cycle at %q", name)
		}
		if state[name] == 2 {
			return nil
		}
		feature, ok := catalog[name]
		if !ok {
			return fmt.Errorf("feature %q is unavailable; install it before running this tool", name)
		}
		state[name] = 1
		for _, dependency := range feature.RequiredFeatures {
			if err := visit(dependency); err != nil {
				return fmt.Errorf("feature %q requires %q: %w", name, dependency, err)
			}
		}
		state[name] = 2
		result = append(result, feature)
		return nil
	}
	roots := append([]string(nil), selected...)
	sort.SliceStable(roots, func(i, j int) bool {
		if catalog[roots[i]].Priority != catalog[roots[j]].Priority {
			return catalog[roots[i]].Priority < catalog[roots[j]].Priority
		}
		return roots[i] < roots[j]
	})
	for _, name := range roots {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	// Choose the lowest-priority ready feature after validating the closure.
	// This keeps ordering independent of whether dependencies were selected
	// explicitly or reached through a consumer.
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		return result[i].Name < result[j].Name
	})
	ordered := make([]model.Extension, 0, len(result))
	installed := map[string]bool{}
	for len(result) > 0 {
		for i, feature := range result {
			ready := true
			for _, dependency := range feature.RequiredFeatures {
				if !installed[dependency] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			ordered = append(ordered, feature)
			installed[feature.Name] = true
			result = append(result[:i], result[i+1:]...)
			break
		}
	}
	return ordered, nil
}

// ResolveRequiredFeatures loads only the tool's required closure. Unrelated optional
// features cannot prevent profile loading or provider credential validation.
func ResolveRequiredFeatures(paths model.Paths, required []string) ([]model.Extension, error) {
	features := []model.Extension{}
	seen := map[string]bool{}
	var load func(string) error
	load = func(name string) error {
		if seen[name] {
			return nil
		}
		seen[name] = true
		feature, err := loadSpecExtension(paths, name, KindMixin)
		if err != nil {
			return fmt.Errorf("required feature %q: %w", name, err)
		}
		features = append(features, feature)
		for _, dependency := range feature.RequiredFeatures {
			if err := load(dependency); err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range required {
		if err := load(name); err != nil {
			return nil, err
		}
	}
	return ResolveFeatureDependencies(features, required, nil)
}
