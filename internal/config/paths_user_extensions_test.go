// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/model"
)

// TestResolveUserExtensionPathsIndependentOfDisk pins that the user extension
// paths say where extensions live, not whether any are installed: `add` needs
// to know where the very first one goes on a machine that has never had the
// directory.
func TestResolveUserExtensionPathsIndependentOfDisk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var paths model.Paths
	resolveUserExtensionPaths(&paths)

	want := HostExtensionsDir(home)
	if paths.UserExtensionsDir != want {
		t.Fatalf("UserExtensionsDir = %q, want %q", paths.UserExtensionsDir, want)
	}
	if paths.UserToolsDir != filepath.Join(want, "tools") {
		t.Fatalf("UserToolsDir = %q", paths.UserToolsDir)
	}
	if paths.UserFeaturesDir != filepath.Join(want, "features") {
		t.Fatalf("UserFeaturesDir = %q", paths.UserFeaturesDir)
	}
	if HasUserExtensions(paths) {
		t.Fatal("HasUserExtensions = true for a root that does not exist")
	}

	if err := os.MkdirAll(want, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", want, err)
	}
	if !HasUserExtensions(paths) {
		t.Fatal("HasUserExtensions = false for an existing root")
	}
}

func TestHasUserExtensionsRejectsUnsetAndNonDirectory(t *testing.T) {
	if HasUserExtensions(model.Paths{}) {
		t.Error("HasUserExtensions = true for unset paths")
	}
	file := filepath.Join(t.TempDir(), "extensions")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	if HasUserExtensions(model.Paths{UserExtensionsDir: file}) {
		t.Error("HasUserExtensions = true for a regular file")
	}
}
