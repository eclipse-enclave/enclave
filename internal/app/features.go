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

	"enclave/internal/backend"
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

	run, _, err := optionsAsRun(ctx.Paths, opts, sources)
	validated := err == nil
	if !validated {
		logx.Warnf("A run with these options would fail: %v", err)
	}
	enabled := make(map[string]bool)
	if !featuresDisabled(run.BuildOptions) {
		for _, name := range selectFeatures(features, run.BuildOptions) {
			enabled[name] = true
		}
	}

	if req != nil && req.JSON {
		selection := &extensionListSelection{
			Directives: opts.Features,
			Source:     sourceLabel(sources.Features),
		}
		if !toolUnset(opts.Tool) {
			selection.Tool = &opts.Tool
		}
		isEnabled := func(name string) bool { return enabled[name] }
		entries := featureListJSONEntries(features, inventory, isEnabled)
		if err := renderExtensionListJSON(os.Stdout, model.KindFeature, selection, entries); err != nil {
			logx.Errorf("%v", err)
			return 1
		}
		return 0
	}

	for _, feature := range features {
		suffix := listingSuffix(inventory[feature.Name])
		switch {
		case enabled[feature.Name]:
			fmt.Printf("✓ %s%s\n", feature.Name, suffix)
		case validated && run.Backend == backend.NameQEMU:
			fmt.Printf("✗ %s%s (disabled by qemu backend%s)\n", feature.Name, suffix, fromSource(sources.Backend, ctx.ProjectDir))
		case run.Slim:
			fmt.Printf("✗ %s%s (disabled by --slim%s)\n", feature.Name, suffix, fromSource(sources.Slim, ctx.ProjectDir))
		case featuresDisabled(run.BuildOptions):
			fmt.Printf("✗ %s%s (disabled by devcontainer%s)\n", feature.Name, suffix, fromSource(sources.Devcontainer, ctx.ProjectDir))
		case run.Features != nil:
			fmt.Printf("✗ %s%s (disabled by features%s)\n", feature.Name, suffix, fromSource(sources.Features, ctx.ProjectDir))
		default:
			fmt.Printf("✗ %s%s (opt-in)\n", feature.Name, suffix)
		}
	}
	return 0
}

func fromSource(source model.OptionSource, projectDir string) string {
	if src := formatSource(source, projectDir); src != "" {
		return " from " + src
	}
	return ""
}
