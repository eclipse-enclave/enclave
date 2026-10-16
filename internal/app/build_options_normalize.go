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

func normalizeConfiguredBuildOptions(paths model.Paths, opts model.BuildOptions) (model.BuildOptions, []string, error) {
	var warnings []string
	if opts.Features != nil || len(opts.FeatureRemovalDirectives) > 0 {
		availableFeatures, err := config.ListFeatures(paths)
		if err != nil {
			return opts, warnings, err
		}
		requested := append(append([]string{}, opts.Features...), opts.FeatureRemovalDirectives...)
		warnings, err = validateFeatureNames(requested, availableFeatures)
		if err != nil {
			return opts, warnings, err
		}
		opts.Features = resolveConfiguredFeatures(opts.Features, availableFeatures)
	}

	return opts, warnings, nil
}
