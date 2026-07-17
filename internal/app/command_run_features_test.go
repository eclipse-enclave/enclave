// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"enclave/internal/model"
)

func TestResolveEnabledFeaturesDefaults(t *testing.T) {
	paths := testPathsWithFeatures(t)

	got := resolveEnabledFeatures(paths, model.BuildOptions{})
	want := []string{"github-cli", "node-dev"}
	if names := extensionNames(got); !reflect.DeepEqual(names, want) {
		t.Fatalf("unexpected default-enabled features: got %v want %v", names, want)
	}
}

func TestResolveEnabledFeaturesExplicitEmpty(t *testing.T) {
	paths := testPathsWithFeatures(t)

	got := resolveEnabledFeatures(paths, model.BuildOptions{Features: []string{}})
	if len(got) != 0 {
		t.Fatalf("expected no enabled features for explicit empty selection, got %v", extensionNames(got))
	}
}

func TestResolveEnabledFeaturesSlim(t *testing.T) {
	paths := testPathsWithFeatures(t)

	got := resolveEnabledFeatures(paths, model.BuildOptions{Slim: true})
	if len(got) != 0 {
		t.Fatalf("expected no enabled features for --slim, got %v", extensionNames(got))
	}
}

func TestResolveEnabledFeaturesDevcontainerDefault(t *testing.T) {
	paths := testPathsWithFeatures(t)

	got := resolveEnabledFeatures(paths, model.BuildOptions{Devcontainer: true})
	if len(got) != 0 {
		t.Fatalf("expected no enabled features for devcontainer default, got %v", extensionNames(got))
	}
}

func TestResolveEnabledFeaturesExplicitSelection(t *testing.T) {
	paths := testPathsWithFeatures(t)

	got := resolveEnabledFeatures(paths, model.BuildOptions{Features: []string{"github-cli"}})
	want := []string{"github-cli"}
	if names := extensionNames(got); !reflect.DeepEqual(names, want) {
		t.Fatalf("unexpected explicit feature result: got %v want %v", names, want)
	}
}

func TestResolveEnabledFeaturesSelectionDefault(t *testing.T) {
	paths := testPathsWithFeatures(t)

	got := resolveEnabledFeatures(paths, model.BuildOptions{Features: []string{model.SelectionDefault}})
	want := []string{"github-cli", "node-dev"}
	if names := extensionNames(got); !reflect.DeepEqual(names, want) {
		t.Fatalf("unexpected default selector feature result: got %v want %v", names, want)
	}
}

func TestResolveEnabledFeaturesSelectionAll(t *testing.T) {
	paths := testPathsWithFeatures(t)

	got := resolveEnabledFeatures(paths, model.BuildOptions{Features: []string{model.FeatureSelectionAll}})
	want := []string{"github-cli", "node-dev", "shell-extras"}
	if names := extensionNames(got); !reflect.DeepEqual(names, want) {
		t.Fatalf("unexpected all selector feature result: got %v want %v", names, want)
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
