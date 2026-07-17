// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"

	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
)

func runFeatures(ctx *AppContext, opts model.Options, sources model.OptionSources) int {
	features, err := config.ListFeatures(ctx.Paths)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	available := map[string]struct{}{}
	if opts.Slim {
		// No features available in slim mode.
	} else if opts.Features != nil {
		selected := resolveConfiguredFeatures(opts.Features, features)
		for _, f := range selected {
			available[f] = struct{}{}
		}
	} else {
		for _, f := range features {
			if f.DefaultEnabled {
				available[f.Name] = struct{}{}
			}
		}
	}

	for _, feature := range features {
		switch {
		case opts.Slim:
			src := formatSource(sources.Slim, ctx.ProjectDir)
			if src != "" {
				fmt.Printf("✗ %s (disabled by --slim from %s)\n", feature.Name, src)
			} else {
				fmt.Printf("✗ %s (disabled by --slim)\n", feature.Name)
			}
		case opts.Features != nil:
			if _, ok := available[feature.Name]; ok {
				fmt.Printf("✓ %s\n", feature.Name)
			} else {
				src := formatSource(sources.Features, ctx.ProjectDir)
				if src != "" {
					fmt.Printf("✗ %s (disabled by features from %s)\n", feature.Name, src)
				} else {
					fmt.Printf("✗ %s (disabled by features)\n", feature.Name)
				}
			}
		default:
			if _, ok := available[feature.Name]; ok {
				fmt.Printf("✓ %s\n", feature.Name)
			} else {
				fmt.Printf("✗ %s (opt-in)\n", feature.Name)
			}
		}
	}
	return 0
}
