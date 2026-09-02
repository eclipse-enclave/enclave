// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
)

const sidecarFixtureSpec = "schemaVersion: \"1\"\nkind: mixin\nname: foo\n"

func sidecarFixturePaths(t *testing.T) model.Paths {
	t.Helper()
	paths := writeTestBuildPaths(t)
	paths.FeaturesDir = filepath.Join(paths.AppRoot, "extensions", "features")
	paths.UserExtensionsDir = filepath.Join(paths.AppRoot, "user", "extensions")
	paths.UserToolsDir = filepath.Join(paths.UserExtensionsDir, "tools")
	paths.UserFeaturesDir = filepath.Join(paths.UserExtensionsDir, "features")
	writeAppFile(t, filepath.Join(paths.FeaturesDir, "foo", "spec.yaml"), sidecarFixtureSpec, 0o644)
	return paths
}

func TestMergedExtensionFilesExcludesSourceSidecar(t *testing.T) {
	paths := sidecarFixturePaths(t)
	writeAppFile(t, filepath.Join(paths.UserFeaturesDir, "foo", "spec.yaml"), sidecarFixtureSpec, 0o644)
	writeAppFile(t, filepath.Join(paths.UserFeaturesDir, "foo", model.ExtensionSourceFilename), `{"commit":"abc"}`, 0o644)

	selection := runtimeImageSelection{Tools: []string{}, Features: []string{"foo"}}
	files, err := mergedExtensionFiles(paths, selection)
	if err != nil {
		t.Fatalf("mergedExtensionFiles: %v", err)
	}
	for _, file := range files {
		if strings.HasSuffix(file.RelativePath, model.ExtensionSourceFilename) {
			t.Fatalf("sidecar leaked into the build context: %s", file.RelativePath)
		}
	}
}

func TestExtensionHashIgnoresSourceSidecar(t *testing.T) {
	paths := sidecarFixturePaths(t)
	writeAppFile(t, filepath.Join(paths.UserFeaturesDir, "foo", "spec.yaml"), sidecarFixtureSpec, 0o644)
	selection := runtimeImageSelection{Tools: []string{}, Features: []string{"foo"}}

	before, err := hashMergedExtensionFiles(paths, selection)
	if err != nil {
		t.Fatalf("hash before: %v", err)
	}
	writeAppFile(t, filepath.Join(paths.UserFeaturesDir, "foo", model.ExtensionSourceFilename), `{"commit":"def"}`, 0o644)
	after, err := hashMergedExtensionFiles(paths, selection)
	if err != nil {
		t.Fatalf("hash after: %v", err)
	}
	if before != after {
		t.Fatalf("image hash changed when only the sidecar changed: %s -> %s", before, after)
	}
}
