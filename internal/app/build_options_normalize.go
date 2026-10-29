// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"enclave/internal/config"
	"enclave/internal/model"
)

// normalizeConfiguredBuildOptions validates and resolves the features
// selection of opts. featureSource is the layer that last set the selection;
// it labels entries without a recorded origin.
func normalizeConfiguredBuildOptions(paths model.Paths, opts model.Options, featureSource model.OptionSource) (model.BuildOptions, []string, error) {
	build := opts.BuildOptions
	var warnings []string
	if build.Features != nil {
		availableFeatures, err := config.ListFeatures(paths)
		if err != nil {
			return build, warnings, err
		}
		warnings, err = validateFeatureNames(featureEntries(opts, featureSource), availableFeatures)
		if err != nil {
			return build, warnings, err
		}
		build.Features = resolveConfiguredFeatures(build.Features, availableFeatures, implicitFeatureBase(availableFeatures, build))
	}

	return build, warnings, nil
}
