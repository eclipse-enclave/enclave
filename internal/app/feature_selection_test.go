// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"reflect"
	"testing"

	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/model"
)

func TestResolveConfiguredFeaturesNil(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
	}

	if got := resolveConfiguredFeatures(nil, available); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestResolveConfiguredFeaturesExplicitList(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
		{Name: "node-dev", DefaultEnabled: true},
	}

	got := resolveConfiguredFeatures([]string{" node-dev ", "devtools", "node-dev"}, available)
	want := []string{"devtools", "node-dev"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected explicit feature normalization: got %v want %v", got, want)
	}
}

func TestResolveConfiguredFeaturesSelectionDefault(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
		{Name: "node-dev", DefaultEnabled: true},
		{Name: "shell-extras", DefaultEnabled: false},
	}

	got := resolveConfiguredFeatures([]string{model.SelectionDefault}, available)
	want := []string{"devtools", "node-dev"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected default selector resolution: got %v want %v", got, want)
	}
}

func TestResolveConfiguredFeaturesSelectionAll(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
		{Name: "node-dev", DefaultEnabled: true},
		{Name: "shell-extras", DefaultEnabled: false},
	}

	got := resolveConfiguredFeatures([]string{model.FeatureSelectionAll}, available)
	want := []string{"devtools", "node-dev", "shell-extras"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected all selector resolution: got %v want %v", got, want)
	}
}

func TestResolveConfiguredFeaturesAdditiveFromImplicitDefaults(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
		{Name: "github-cli", DefaultEnabled: true},
		{Name: "node-dev", DefaultEnabled: true},
		{Name: "python-dev", DefaultEnabled: true},
		{Name: "shell-extras", DefaultEnabled: false},
	}

	got := resolveConfiguredFeatures([]string{"-node-dev", "+shell-extras"}, available)
	want := []string{"devtools", "github-cli", "python-dev", "shell-extras"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected additive feature resolution: got %v want %v", got, want)
	}
}

func TestResolveConfiguredFeaturesAdditiveRemoveAllDefaults(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
		{Name: "github-cli", DefaultEnabled: true},
	}

	got := resolveConfiguredFeatures([]string{"-devtools", "-github-cli"}, available)
	want := []string{}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected additive feature resolution: got %v want %v", got, want)
	}
}

func TestResolveConfiguredFeaturesDefaultPlusExplicit(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
		{Name: "node-dev", DefaultEnabled: true},
		{Name: "playwright", DefaultEnabled: false},
		{Name: "shell-extras", DefaultEnabled: false},
	}

	got := resolveConfiguredFeatures([]string{"default", "playwright"}, available)
	want := []string{"devtools", "node-dev", "playwright"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected default+explicit resolution: got %v want %v", got, want)
	}
}

func TestResolveConfiguredFeaturesDefaultPlusRemoval(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
		{Name: "node-dev", DefaultEnabled: true},
		{Name: "shell-extras", DefaultEnabled: false},
	}

	got := resolveConfiguredFeatures([]string{"default", "-node-dev", "shell-extras"}, available)
	want := []string{"devtools", "shell-extras"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected default+removal resolution: got %v want %v", got, want)
	}
}

func TestResolveConfiguredFeaturesLiteralListWithDirectives(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
		{Name: "node-dev", DefaultEnabled: true},
		{Name: "shell-extras", DefaultEnabled: false},
	}

	// A configured literal list amended by a higher-precedence directive: the
	// literal entries anchor the set, so the defaults are not pulled back in.
	got := resolveConfiguredFeatures([]string{"shell-extras", "+devtools"}, available)
	want := []string{"devtools", "shell-extras"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected literal+directive resolution: got %v want %v", got, want)
	}
}

func TestResolveConfiguredFeaturesAllPlusRemoval(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
		{Name: "node-dev", DefaultEnabled: true},
		{Name: "shell-extras", DefaultEnabled: false},
	}

	got := resolveConfiguredFeatures([]string{"all", "-shell-extras"}, available)
	want := []string{"devtools", "node-dev"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected all+removal resolution: got %v want %v", got, want)
	}
}

// End-to-end over the whole chain a run takes: parse the flag, layer the config
// scopes, then expand the merged selection against the available features.
func TestFeatureSelectionFromCLIAndConfigLayers(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
		{Name: "github-cli", DefaultEnabled: true},
		{Name: "node-dev", DefaultEnabled: true},
		{Name: "playwright", DefaultEnabled: false},
		{Name: "shell-extras", DefaultEnabled: false},
	}

	cases := []struct {
		name    string
		args    []string
		global  config.Defaults
		project config.Defaults
		want    []string
	}{
		{
			name:   "additive flag keeps configured additions and removals",
			args:   []string{"--features", "+playwright"},
			global: config.Defaults{Features: []string{"+shell-extras", "-node-dev"}},
			want:   []string{"devtools", "github-cli", "playwright", "shell-extras"},
		},
		{
			name:   "additive flag keeps a configured literal list",
			args:   []string{"--features", "+playwright"},
			global: config.Defaults{Features: []string{"node-dev"}},
			want:   []string{"node-dev", "playwright"},
		},
		{
			name:   "flag removing the only configured literal leaves nothing",
			args:   []string{"--features", "-node-dev"},
			global: config.Defaults{Features: []string{"node-dev"}},
			want:   []string{},
		},
		{
			name:   "flag re-adding a configured literal keeps the list anchored",
			args:   []string{"--features", "+node-dev"},
			global: config.Defaults{Features: []string{"node-dev"}},
			want:   []string{"node-dev"},
		},
		{
			name:    "project removing the only global literal leaves nothing",
			args:    nil,
			global:  config.Defaults{Features: []string{"node-dev"}},
			project: config.Defaults{Features: []string{"-node-dev"}},
			want:    []string{},
		},
		{
			name:   "additive flag on top of configured none adds only itself",
			args:   []string{"--features", "+playwright"},
			global: config.Defaults{Features: []string{}},
			want:   []string{"playwright"},
		},
		{
			name:   "unprefixed flag replaces the configured selection",
			args:   []string{"--features", "github-cli"},
			global: config.Defaults{Features: []string{"+shell-extras", "-node-dev"}},
			want:   []string{"github-cli"},
		},
		{
			name:   "none replaces the configured selection",
			args:   []string{"--features", "none"},
			global: config.Defaults{Features: []string{"+shell-extras"}},
			want:   []string{},
		},
		{
			name:    "project layer wins a conflict with global",
			args:    []string{"--features", "+playwright"},
			global:  config.Defaults{Features: []string{"-devtools"}},
			project: config.Defaults{Features: []string{"+devtools"}},
			want:    []string{"devtools", "github-cli", "node-dev", "playwright"},
		},
		{
			name:   "flag wins a conflict with config",
			args:   []string{"--features", "+node-dev"},
			global: config.Defaults{Features: []string{"-node-dev"}},
			want:   []string{"devtools", "github-cli", "node-dev"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := cli.Parse(tc.args, config.DefaultOptions())
			if err != nil {
				t.Fatalf("parse %v: %v", tc.args, err)
			}
			opts, _, _ := config.ResolveOptionsForTool(res.Options, res.Sources, tc.global, tc.project, "")
			got := resolveConfiguredFeatures(opts.Features, available)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("resolved features: got %v want %v", got, tc.want)
			}
		})
	}
}
