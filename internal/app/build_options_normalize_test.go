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
	"testing"

	"enclave/internal/model"
)

func TestNormalizeConfiguredBuildOptions_NoFeatures(t *testing.T) {
	paths := testPathsWithFeatureDefaults(t)
	opts := model.BuildOptions{}

	got, err := normalizeConfiguredBuildOptions(paths, opts)
	if err != nil {
		t.Fatalf("normalizeConfiguredBuildOptions: %v", err)
	}
	if got.Features != nil {
		t.Fatalf("expected nil features when unset, got %v", got.Features)
	}
}

func TestNormalizeConfiguredBuildOptions_AdditiveAgainstImplicitDefaults(t *testing.T) {
	paths := testPathsWithFeatureDefaults(t)
	opts := model.BuildOptions{
		Features: []string{"-node-dev", "+shell-extras"},
	}

	got, err := normalizeConfiguredBuildOptions(paths, opts)
	if err != nil {
		t.Fatalf("normalizeConfiguredBuildOptions: %v", err)
	}

	want := []string{"github-cli", "shell-extras"}
	if !reflect.DeepEqual(got.Features, want) {
		t.Fatalf("unexpected normalized features: got %v want %v", got.Features, want)
	}
}

func testPathsWithFeatureDefaults(t *testing.T) model.Paths {
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
