// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"enclave/internal/config"
	"enclave/internal/model"
)

// validateFeatureNames checks a features selection against the installed
// features: unknown additions fail and unknown removals only warn. Each message
// names where its entries were set, as label reports it.
func validateFeatureNames(requested []string, allFeatures []model.Extension, label func(entry string) string) ([]string, error) {
	available := make(map[string]struct{}, len(allFeatures))
	for _, feature := range allFeatures {
		available[feature.Name] = struct{}{}
	}

	var unknown, unknownRemovals namesByLabel
	for _, raw := range requested {
		name := strings.TrimSpace(raw)
		removal := strings.HasPrefix(name, "-")
		if removal || strings.HasPrefix(name, "+") {
			name = strings.TrimSpace(name[1:])
		}
		if name == "" {
			continue
		}
		if !removal {
			switch strings.ToLower(name) {
			case model.SelectionDefault, model.FeatureSelectionAll:
				continue
			}
		}
		if _, ok := available[name]; ok {
			continue
		}
		if removal {
			unknownRemovals.add(label(raw), name)
		} else {
			unknown.add(label(raw), name)
		}
	}

	var warnings []string
	for _, group := range unknownRemovals {
		warnings = append(warnings, fmt.Sprintf("%s: ignoring removal of unknown features: %s; run 'enclave features list' to see available features", group.label, strings.Join(normalizeAndSortNames(group.names), ", ")))
	}
	var errs []error
	for _, group := range unknown {
		errs = append(errs, fmt.Errorf("%s: unknown features: %s; run 'enclave features list' to see available features", group.label, strings.Join(normalizeAndSortNames(group.names), ", ")))
	}
	return warnings, errors.Join(errs...)
}

// namesByLabel groups names by label, in the order the labels first appear.
type namesByLabel []struct {
	label string
	names []string
}

func (g *namesByLabel) add(label string, name string) {
	for i := range *g {
		if (*g)[i].label == label {
			(*g)[i].names = append((*g)[i].names, name)
			return
		}
	}
	*g = append(*g, struct {
		label string
		names []string
	}{label: label, names: []string{name}})
}

// featureEntryLabels names where each entry of a features selection was set:
// the flag or config file that option resolution recorded, else the layer that
// last set the list.
func featureEntryLabels(origins model.ListOrigins, source model.OptionSource) func(entry string) string {
	fallback := "features"
	switch source {
	case model.SourceCLI:
		fallback = "--features"
	case model.SourceGlobal:
		fallback = "features in global config"
	case model.SourceProject:
		fallback = "features in project config"
	case model.SourceToolOverride:
		fallback = "features in tool override"
	}
	return func(entry string) string {
		if origin, ok := origins.Origin("features", entry); ok {
			return config.ListOriginLabel("features", origin)
		}
		return fallback
	}
}

// featureSelection is what a build selects among the available features:
// the selected names, including those that match no feature, the matching
// features in listing order, and the unknown names in selection order.
type featureSelection struct {
	selected []string
	enabled  []model.Extension
	unknown  []string
}

func resolveFeatureSelection(paths model.Paths, build model.BuildOptions) (featureSelection, error) {
	if featuresDisabled(build) {
		return featureSelection{selected: []string{}}, nil
	}
	allFeatures, err := config.ListFeatures(paths)
	if err != nil {
		return featureSelection{}, err
	}
	selection := featureSelection{selected: selectFeatures(allFeatures, build)}
	requested := make(map[string]bool, len(selection.selected))
	for _, name := range selection.selected {
		requested[name] = true
	}
	available := make(map[string]bool, len(allFeatures))
	for _, feature := range allFeatures {
		available[feature.Name] = true
		if requested[feature.Name] {
			selection.enabled = append(selection.enabled, feature)
		}
	}
	for _, name := range selection.selected {
		if !available[name] {
			selection.unknown = append(selection.unknown, name)
		}
	}
	return selection, nil
}

// featureSelectionWarnings describes why a selection resolved with err is
// incomplete: the features cannot be listed, or selected names are unknown.
func featureSelectionWarnings(selection featureSelection, err error) []configJSONWarning {
	if err != nil {
		return []configJSONWarning{{Code: configWarningFeaturesUnavailable, Option: "features", Message: fmt.Sprintf("Cannot list features: %v", err)}}
	}
	warnings := make([]configJSONWarning, 0, len(selection.unknown))
	for _, name := range selection.unknown {
		warnings = append(warnings, configJSONWarning{
			Code:    configWarningUnknownFeature,
			Option:  "features",
			Message: fmt.Sprintf("Unknown feature %q selected; it is ignored", name),
		})
	}
	return warnings
}

// enabledNames returns the sorted names of the enabled features.
func (s featureSelection) enabledNames() []string {
	names := featureNameList(s.enabled)
	sort.Strings(names)
	return names
}

func selectFeatures(allFeatures []model.Extension, build model.BuildOptions) []string {
	if build.Features == nil {
		return defaultEnabledFeatureNames(allFeatures)
	}
	return resolveConfiguredFeatures(build.Features, allFeatures, implicitFeatureBase(allFeatures, build))
}

// implicitFeatureBase is what a list of only +/- directives amends: the
// default-enabled features, or none in devcontainer mode.
func implicitFeatureBase(allFeatures []model.Extension, build model.BuildOptions) []string {
	if build.Devcontainer {
		return []string{}
	}
	return defaultEnabledFeatureNames(allFeatures)
}

func featuresDisabled(build model.BuildOptions) bool {
	return build.Slim || (build.Devcontainer && build.Features == nil)
}

func resolveConfiguredFeatures(requested []string, allFeatures []model.Extension, implicitBase []string) []string {
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
		return expandFeatureDirectives(normalized, allFeatures, implicitBase)
	}
	return normalized
}

// expandFeatureDirectives resolves a mixed list of selectors ("default",
// "all"), additive directives (+/-), and literal feature names into a flat
// sorted list of feature names.
//
// Processing uses two passes so that removals always apply after additions
// regardless of list order (normalizeAndSortNames sorts alphabetically).
func expandFeatureDirectives(normalized []string, allFeatures []model.Extension, implicitBase []string) []string {
	selected := make(map[string]struct{}, len(allFeatures))

	// Only a list of nothing but directives amends the implicit base; a bare
	// name or selector makes the list an explicit selection.
	if config.IsDirectiveOnly(normalized) {
		for _, name := range implicitBase {
			selected[name] = struct{}{}
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
