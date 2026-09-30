// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"strings"

	"enclave/internal/config"
	"enclave/internal/model"
)

func normalizeConfiguredBuildOptions(paths model.Paths, opts model.BuildOptions, tool string) (model.BuildOptions, error) {
	opts.ResolvedFeatures = nil
	selected, err := resolveSelectedFeatures(paths, opts, tool)
	if err != nil {
		return opts, err
	}
	opts.ResolvedFeatures = featureNameList(selected)
	return opts, nil
}

func resolveSelectedFeatures(paths model.Paths, opts model.BuildOptions, tool string) ([]model.Extension, error) {
	features, err := config.ListFeatures(paths)
	if err != nil {
		return nil, err
	}
	if opts.ResolvedFeatures != nil {
		return config.ResolveFeatureDependencies(features, opts.ResolvedFeatures, nil)
	}
	selected := []string{}
	if !opts.Slim && (!opts.Devcontainer || opts.Features != nil) {
		if opts.Features == nil {
			selected = defaultEnabledFeatureNames(features)
		} else {
			selected = resolveConfiguredFeatures(opts.Features, features)
		}
	}
	excluded := map[string]bool{}
	for _, name := range opts.Features {
		name = strings.TrimSpace(name)
		if strings.HasPrefix(name, "-") {
			excluded[strings.TrimSpace(name[1:])] = true
		}
	}
	if tool != "" {
		doc, err := config.LoadSpec(paths, tool, config.KindSandbox)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			selected = append(selected, doc.RequiresFeatures...)
		}
	}
	return config.ResolveFeatureDependencies(features, selected, excluded)
}
