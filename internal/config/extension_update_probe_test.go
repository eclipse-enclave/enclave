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

// TestUpdateProbeApplies pins the rule that only a sandbox extension's
// check-update.sh is ever executed, which ResolveUpdateProbe implements by
// searching the tool roots alone and extinstall discloses to users.
func TestUpdateProbeApplies(t *testing.T) {
	if !UpdateProbeApplies(model.ExtensionKindSandbox) {
		t.Error("UpdateProbeApplies(sandbox) = false, want true")
	}
	if UpdateProbeApplies(model.ExtensionKindMixin) {
		t.Error("UpdateProbeApplies(mixin) = true, want false")
	}
	if UpdateProbeApplies("") {
		t.Error("UpdateProbeApplies(\"\") = true, want false")
	}
}

// TestResolveUpdateProbeSearchesToolRootsOnly is the other half of the rule: a
// mixin shipping the same filename is never resolved, so it is never run.
func TestResolveUpdateProbeSearchesToolRootsOnly(t *testing.T) {
	root := t.TempDir()
	paths := model.Paths{
		ToolsDir:    filepath.Join(root, "extensions", "tools"),
		FeaturesDir: filepath.Join(root, "extensions", "features"),
	}
	for _, dir := range []string{filepath.Join(paths.ToolsDir, "atool"), filepath.Join(paths.FeaturesDir, "afeature")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, model.CheckUpdateScriptFilename), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatalf("write probe in %s: %v", dir, err)
		}
	}

	if _, found := ResolveUpdateProbe(paths, "atool"); !found {
		t.Error("a tool's check-update.sh was not resolved")
	}
	if _, found := ResolveUpdateProbe(paths, "afeature"); found {
		t.Error("a feature's check-update.sh was resolved; it must never be executed")
	}
}
