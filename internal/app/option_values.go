// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"enclave/internal/config"
	"enclave/internal/filterhost"
	"enclave/internal/logx"
	"enclave/internal/model"
)

// typedOptionValue returns the value of def that `config --json` reports and
// a session records as provenance, plus the input that decided yolo mode.
// values must hold validated options with host_config_paths resolved against
// profile, which is nil when no tool profile is loaded; features are the
// enabled feature names.
func typedOptionValue(def config.OptionDef, values model.Options, profile *model.Profile, features []string) (value any, resolvedBy string) {
	switch {
	case def.Kind == config.OptionKindYolo:
		if profile == nil {
			return nil, ""
		}
		return resolveYolo(*profile, values)
	case def.Apply == config.ApplySliceMergeFeature:
		return features, ""
	case def.Apply == config.ApplySliceMergeFilter:
		return selectedFilterLabels(values.Filters), ""
	case def.Apply == config.ApplySliceMergeHost:
		if profile == nil {
			return nil, ""
		}
		return append([]string{}, values.HostConfigPaths...), ""
	}
	return config.OptionTypedEffectiveValue(def, values), ""
}

// sessionFeaturesAndProvenance returns the feature selection a session
// builds with and, for run actions, the provenance it records. Provenance is
// nil when features cannot be listed.
func sessionFeaturesAndProvenance(input *CommandInput, opts model.Options, profile model.Profile) (featureSelection, map[string]any) {
	selection, err := resolveFeatureSelection(input.Ctx.Paths, opts.BuildOptions)
	for _, warning := range featureSelectionWarnings(selection, err) {
		logx.Debugf("%s", warning.Message)
	}
	if err != nil {
		return selection, nil
	}
	if !isRunAction(input.Action) {
		return selection, nil
	}
	opts.Backend = input.ConfiguredBackend
	opts.ImageName = input.Options.ImageName
	return selection, sessionProvenance(opts, profile, selection)
}

// sessionProvenance returns the session-affecting option values a session
// records on its container, typed as `config --json` reports them.
func sessionProvenance(opts model.Options, profile model.Profile, selection featureSelection) map[string]any {
	features := selection.enabledNames()
	options := map[string]any{}
	for _, def := range config.OptionDefs() {
		if def.AffectsSession == config.SessionAffected {
			options[def.Name], _ = typedOptionValue(def, opts, &profile, features)
		}
	}
	return options
}

// selectedFilterLabels is the filters selection as name or name:preset
// entries, directives applied. A filter without a preset uses its default
// one, which only loading the filter can name.
func selectedFilterLabels(entries []string) []string {
	selections, err := filterhost.ParseSelection(entries)
	if err != nil {
		return append([]string{}, entries...)
	}
	labels := make([]string, 0, len(selections))
	for _, sel := range selections {
		labels = append(labels, sel.String())
	}
	return labels
}
