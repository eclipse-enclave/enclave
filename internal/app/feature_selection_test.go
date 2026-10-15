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

func TestResolveFeatureSelection(t *testing.T) {
	paths := testPathsWithFeatures(t)

	cases := []struct {
		name        string
		build       model.BuildOptions
		wantEnabled []string
		wantUnknown []string
	}{
		// resolveConfiguredFeatures maps an explicit empty selection to nil.
		{name: "explicit empty", build: model.BuildOptions{Features: []string{}}, wantEnabled: nil},
		{name: "slim", build: model.BuildOptions{Slim: true, Features: []string{"github-cli"}}, wantEnabled: []string{}},
		{name: "devcontainer unset", build: model.BuildOptions{Devcontainer: true}, wantEnabled: []string{}},
		{name: "devcontainer explicit", build: model.BuildOptions{Devcontainer: true, Features: []string{"shell-extras"}}, wantEnabled: []string{"shell-extras"}},
		{name: "additive", build: model.BuildOptions{Features: []string{"+shell-extras", "-node-dev"}}, wantEnabled: []string{"github-cli", "shell-extras"}},
		{name: "unknown literal", build: model.BuildOptions{Features: []string{"ghost", "github-cli"}}, wantEnabled: []string{"ghost", "github-cli"}, wantUnknown: []string{"ghost"}},
		{name: "unknown directive", build: model.BuildOptions{Features: []string{"+ghost"}}, wantEnabled: []string{"ghost", "github-cli", "node-dev"}, wantUnknown: []string{"ghost"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enabled, unknown, err := resolveFeatureSelection(paths, tc.build)
			if err != nil {
				t.Fatalf("resolveFeatureSelection: %v", err)
			}
			if !reflect.DeepEqual(enabled, tc.wantEnabled) {
				t.Fatalf("enabled = %#v, want %#v", enabled, tc.wantEnabled)
			}
			if !reflect.DeepEqual(unknown, tc.wantUnknown) {
				t.Fatalf("unknown = %#v, want %#v", unknown, tc.wantUnknown)
			}
		})
	}
}
