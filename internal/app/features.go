// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"

	"enclave/internal/config"
	"enclave/internal/extinstall"
	"enclave/internal/logx"
	"enclave/internal/model"
)

func runFeatures(ctx *AppContext, req *extinstall.Request, opts model.Options, sources model.OptionSources) int {
	features, err := config.ListFeatures(ctx.Paths)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	inventory, err := extensionInventory(ctx.Paths, model.KindFeature, features)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}

	available := map[string]struct{}{}
	selected, err := resolveSelectedFeatures(ctx.Paths, opts.BuildOptions, opts.Tool)
	if err != nil {
		logx.Errorf("%v", err)
		return 1
	}
	for _, feature := range selected {
		available[feature.Name] = struct{}{}
	}

	if req != nil && req.JSON {
		isAvailable := func(name string) bool {
			_, ok := available[name]
			return ok
		}
		entries := extensionListJSONEntries(features, inventory, isAvailable)
		if err := renderExtensionListJSON(os.Stdout, model.KindFeature, entries); err != nil {
			logx.Errorf("%v", err)
			return 1
		}
		return 0
	}

	for _, feature := range features {
		suffix := provenanceSuffix(inventory[feature.Name])
		_, enabled := available[feature.Name]
		switch {
		case enabled:
			fmt.Printf("✓ %s%s\n", feature.Name, suffix)
		case opts.Slim:
			src := formatSource(sources.Slim, ctx.ProjectDir)
			if src != "" {
				fmt.Printf("✗ %s%s (disabled by --slim from %s)\n", feature.Name, suffix, src)
			} else {
				fmt.Printf("✗ %s%s (disabled by --slim)\n", feature.Name, suffix)
			}
		case opts.Features != nil:
			src := formatSource(sources.Features, ctx.ProjectDir)
			if src != "" {
				fmt.Printf("✗ %s%s (disabled by features from %s)\n", feature.Name, suffix, src)
			} else {
				fmt.Printf("✗ %s%s (disabled by features)\n", feature.Name, suffix)
			}
		default:
			fmt.Printf("✗ %s%s (opt-in)\n", feature.Name, suffix)
		}
	}
	return 0
}
