// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"path/filepath"
	"sort"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

func TestExplicitEmptyFeaturesSurviveValidation(t *testing.T) {
	paths := testPathsWithFeatures(t)
	base := model.Options{
		RunOptions:   model.RunOptions{Tool: "claude"},
		BuildOptions: model.BuildOptions{ImageName: "test"},
	}

	cli := base
	cli.Features = []string{}
	cliSources := model.DefaultOptionSources()
	cliSources.Features = model.SourceCLI

	fromConfig, _, _ := config.ResolveOptionsForTool(base, model.OptionSources{}, config.Defaults{Features: []string{}}, config.Defaults{}, "")

	cases := []struct {
		name    string
		opts    model.Options
		sources model.OptionSources
	}{
		{name: "cli --features none", opts: cli, sources: cliSources},
		{name: "config features []", opts: fromConfig, sources: fromConfig.Sources},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _, err := ValidateOptions(tc.opts, tc.sources, ValidationContext{Action: "run", Paths: paths})
			if err != nil {
				t.Fatalf("ValidateOptions: %v", err)
			}
			if got.Features == nil || len(got.Features) != 0 {
				t.Fatalf("Features = %#v, want explicit empty", got.Features)
			}
			features, err := resolveFeatureSelection(paths, got.BuildOptions)
			if err != nil {
				t.Fatalf("resolveFeatureSelection: %v", err)
			}
			if len(features.enabled) != 0 {
				t.Fatalf("enabled features = %v, want none", extensionNames(features.enabled))
			}
			selection, err := resolveRuntimeImageSelection(paths, got.BuildOptions, got.Tool)
			if err != nil {
				t.Fatalf("resolveRuntimeImageSelection: %v", err)
			}
			if selection.Features == nil || len(selection.Features) != 0 {
				t.Fatalf("selection.Features = %#v, want explicit empty", selection.Features)
			}
			if arg := resolveFeaturesArg(got.BuildOptions); arg != "" {
				t.Fatalf("resolveFeaturesArg = %q, want empty", arg)
			}
		})
	}
}

func testPathsWithFeatures(t *testing.T) model.Paths {
	t.Helper()
	root := t.TempDir()
	featuresDir := filepath.Join(root, "extensions", "features")

	writeAppFile(t, filepath.Join(featuresDir, "node-dev", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: node-dev\ndefaultEnabled: true\n", 0o644)
	writeAppFile(t, filepath.Join(featuresDir, "github-cli", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: github-cli\ndefaultEnabled: true\n", 0o644)
	writeAppFile(t, filepath.Join(featuresDir, "shell-extras", "spec.yaml"), "schemaVersion: \"1\"\nkind: mixin\nname: shell-extras\ndefaultEnabled: false\n", 0o644)

	return model.Paths{
		FeaturesDir: featuresDir,
	}
}

func extensionNames(features []model.Extension) []string {
	names := make([]string, 0, len(features))
	for _, feature := range features {
		names = append(names, feature.Name)
	}
	sort.Strings(names)
	return names
}
