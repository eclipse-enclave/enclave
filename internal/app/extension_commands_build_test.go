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

// hostCommandRel is where an extension's host verbs live inside its directory.
func hostCommandRel(name string) string {
	return filepath.Join(model.CommandsDirName, model.CommandsHostDirName, name)
}

// Host commands run on the host, never in the image, so shipping them into the
// build context would bake a script the container cannot use.
func TestMergedExtensionFilesExcludesHostCommands(t *testing.T) {
	paths := sidecarFixturePaths(t)
	writeAppFile(t, filepath.Join(paths.UserFeaturesDir, "foo", "spec.yaml"), sidecarFixtureSpec, 0o644)
	writeAppFile(t, filepath.Join(paths.UserFeaturesDir, "foo", hostCommandRel("vnc-viewer")), "#!/bin/sh\n", 0o755)

	selection := runtimeImageSelection{Tools: []string{}, Features: []string{"foo"}}
	files, err := mergedExtensionFiles(paths, selection)
	if err != nil {
		t.Fatalf("mergedExtensionFiles: %v", err)
	}
	for _, file := range files {
		if strings.Contains(file.RelativePath, "/"+model.CommandsDirName+"/") {
			t.Fatalf("host command leaked into the build context: %s", file.RelativePath)
		}
	}
}

// Editing a host command must not force an image rebuild: nothing about the
// image depends on it.
func TestExtensionHashIgnoresHostCommands(t *testing.T) {
	paths := sidecarFixturePaths(t)
	writeAppFile(t, filepath.Join(paths.UserFeaturesDir, "foo", "spec.yaml"), sidecarFixtureSpec, 0o644)
	writeAppFile(t, filepath.Join(paths.UserFeaturesDir, "foo", hostCommandRel("vnc-viewer")), "#!/bin/sh\n", 0o755)
	selection := runtimeImageSelection{Tools: []string{}, Features: []string{"foo"}}

	before, err := hashMergedExtensionFiles(paths, selection)
	if err != nil {
		t.Fatalf("hash before: %v", err)
	}
	writeAppFile(t, filepath.Join(paths.UserFeaturesDir, "foo", hostCommandRel("vnc-viewer")),
		"#!/bin/sh\necho rewritten\n", 0o755)
	after, err := hashMergedExtensionFiles(paths, selection)
	if err != nil {
		t.Fatalf("hash after: %v", err)
	}
	if before != after {
		t.Fatalf("image hash changed when only a host command changed: %s -> %s", before, after)
	}
}

// A built-in extension's commands/ is skipped for the same reason, so the
// exclusion cannot be worked around by shadowing a built-in.
func TestMergedExtensionFilesExcludesBuiltinHostCommands(t *testing.T) {
	paths := sidecarFixturePaths(t)
	writeAppFile(t, filepath.Join(paths.FeaturesDir, "foo", hostCommandRel("vnc-viewer")), "#!/bin/sh\n", 0o755)

	selection := runtimeImageSelection{Tools: []string{}, Features: []string{"foo"}}
	files, err := mergedExtensionFiles(paths, selection)
	if err != nil {
		t.Fatalf("mergedExtensionFiles: %v", err)
	}
	for _, file := range files {
		if strings.Contains(file.RelativePath, "/"+model.CommandsDirName+"/") {
			t.Fatalf("host command leaked into the build context: %s", file.RelativePath)
		}
	}
}
