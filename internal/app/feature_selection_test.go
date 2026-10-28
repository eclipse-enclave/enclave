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

	"enclave/internal/model"
)

func TestResolveConfiguredFeaturesExplicitList(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
		{Name: "node-dev", DefaultEnabled: true},
	}

	got := resolveConfiguredFeatures([]string{" node-dev ", "devtools", "node-dev"}, available, defaultEnabledFeatureNames(available))
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

	got := resolveConfiguredFeatures([]string{model.SelectionDefault}, available, defaultEnabledFeatureNames(available))
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

	got := resolveConfiguredFeatures([]string{model.FeatureSelectionAll}, available, defaultEnabledFeatureNames(available))
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

	got := resolveConfiguredFeatures([]string{"-node-dev", "+shell-extras"}, available, defaultEnabledFeatureNames(available))
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

	got := resolveConfiguredFeatures([]string{"-devtools", "-github-cli"}, available, defaultEnabledFeatureNames(available))
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

	got := resolveConfiguredFeatures([]string{"default", "playwright"}, available, defaultEnabledFeatureNames(available))
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

	got := resolveConfiguredFeatures([]string{"default", "-node-dev", "shell-extras"}, available, defaultEnabledFeatureNames(available))
	want := []string{"devtools", "shell-extras"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected default+removal resolution: got %v want %v", got, want)
	}
}

func TestResolveConfiguredFeaturesAllPlusRemoval(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
		{Name: "node-dev", DefaultEnabled: true},
		{Name: "shell-extras", DefaultEnabled: false},
	}

	got := resolveConfiguredFeatures([]string{"all", "-shell-extras"}, available, defaultEnabledFeatureNames(available))
	want := []string{"devtools", "node-dev"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected all+removal resolution: got %v want %v", got, want)
	}
}

func TestResolveFeatureSelection(t *testing.T) {
	paths := testPathsWithFeatures(t)

	cases := []struct {
		name         string
		build        model.BuildOptions
		wantSelected []string
		wantEnabled  []string
		wantUnknown  []string
	}{
		{name: "defaults", build: model.BuildOptions{}, wantSelected: []string{"github-cli", "node-dev"}, wantEnabled: []string{"github-cli", "node-dev"}},
		{name: "explicit selection", build: model.BuildOptions{Features: []string{"github-cli"}}, wantSelected: []string{"github-cli"}, wantEnabled: []string{"github-cli"}},
		{name: "default selector", build: model.BuildOptions{Features: []string{model.SelectionDefault}}, wantSelected: []string{"github-cli", "node-dev"}, wantEnabled: []string{"github-cli", "node-dev"}},
		{name: "all selector", build: model.BuildOptions{Features: []string{model.FeatureSelectionAll}}, wantSelected: []string{"github-cli", "node-dev", "shell-extras"}, wantEnabled: []string{"github-cli", "node-dev", "shell-extras"}},
		{name: "explicit empty", build: model.BuildOptions{Features: []string{}}, wantSelected: []string{}, wantEnabled: []string{}},
		{name: "slim", build: model.BuildOptions{Slim: true, Features: []string{"github-cli"}}, wantSelected: []string{}, wantEnabled: []string{}},
		{name: "devcontainer unset", build: model.BuildOptions{Devcontainer: true}, wantSelected: []string{}, wantEnabled: []string{}},
		{name: "devcontainer explicit", build: model.BuildOptions{Devcontainer: true, Features: []string{"shell-extras"}}, wantSelected: []string{"shell-extras"}, wantEnabled: []string{"shell-extras"}},
		{name: "additive", build: model.BuildOptions{Features: []string{"+shell-extras", "-node-dev"}}, wantSelected: []string{"github-cli", "shell-extras"}, wantEnabled: []string{"github-cli", "shell-extras"}},
		{name: "unknown literal", build: model.BuildOptions{Features: []string{"ghost", "github-cli"}}, wantSelected: []string{"ghost", "github-cli"}, wantEnabled: []string{"github-cli"}, wantUnknown: []string{"ghost"}},
		{name: "unknown directive", build: model.BuildOptions{Features: []string{"+ghost"}}, wantSelected: []string{"ghost", "github-cli", "node-dev"}, wantEnabled: []string{"github-cli", "node-dev"}, wantUnknown: []string{"ghost"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selection, err := resolveFeatureSelection(paths, tc.build)
			if err != nil {
				t.Fatalf("resolveFeatureSelection: %v", err)
			}
			if !reflect.DeepEqual(selection.selected, tc.wantSelected) {
				t.Fatalf("selected = %#v, want %#v", selection.selected, tc.wantSelected)
			}
			if enabled := extensionNames(selection.enabled); !reflect.DeepEqual(enabled, tc.wantEnabled) {
				t.Fatalf("enabled = %#v, want %#v", enabled, tc.wantEnabled)
			}
			if !reflect.DeepEqual(selection.unknown, tc.wantUnknown) {
				t.Fatalf("unknown = %#v, want %#v", selection.unknown, tc.wantUnknown)
			}
		})
	}
}

func TestFeatureSelectionEnabledNamesSorted(t *testing.T) {
	selection := featureSelection{enabled: []model.Extension{{Name: "node-dev"}, {Name: "github-cli"}}}
	if got := selection.enabledNames(); !reflect.DeepEqual(got, []string{"github-cli", "node-dev"}) {
		t.Fatalf("enabledNames() = %#v", got)
	}
}

func TestResolveConfiguredFeaturesImplicitBaseOnlyForDirectives(t *testing.T) {
	available := []model.Extension{
		{Name: "devtools", DefaultEnabled: true},
		{Name: "node-dev", DefaultEnabled: true},
		{Name: "debug-tools"},
	}
	defaults := defaultEnabledFeatureNames(available)

	// A bare name makes the list explicit, so its removal does not bring the defaults back.
	if got := resolveConfiguredFeatures([]string{"node-dev", "-debug-tools"}, available, defaults); !reflect.DeepEqual(got, []string{"node-dev"}) {
		t.Fatalf("explicit list with removal = %v, want [node-dev]", got)
	}
	if got := resolveConfiguredFeatures([]string{"+debug-tools"}, available, defaults); !reflect.DeepEqual(got, []string{"debug-tools", "devtools", "node-dev"}) {
		t.Fatalf("directives = %v, want defaults plus debug-tools", got)
	}
	devcontainer := implicitFeatureBase(available, model.BuildOptions{Devcontainer: true})
	if got := resolveConfiguredFeatures([]string{"+debug-tools"}, available, devcontainer); !reflect.DeepEqual(got, []string{"debug-tools"}) {
		t.Fatalf("devcontainer directives = %v, want [debug-tools]", got)
	}
}
