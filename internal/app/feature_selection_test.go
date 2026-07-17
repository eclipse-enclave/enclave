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
