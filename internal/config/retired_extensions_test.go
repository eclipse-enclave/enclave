// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"enclave/internal/model"
)

func TestRetiredExtensionHint(t *testing.T) {
	for _, tc := range []struct {
		kind model.ExtensionKind
		name string
		want string
	}{
		{model.KindTool, "mistral-vibe", "tool mistral-vibe moved to enclave-extensions: enclave tools add eclipse-enclave/enclave-extensions --name mistral-vibe"},
		{model.KindFeature, "mistral-vibe", ""},
		{model.KindFeature, "playwright", "feature playwright moved to enclave-extensions as playwright-mcp: enclave features add eclipse-enclave/enclave-extensions --name playwright-mcp"},
		{model.KindFeature, "shell-extras", "feature shell-extras moved to enclave-extensions: enclave features add eclipse-enclave/enclave-extensions --name shell-extras"},
		{model.KindFeature, "native-build-deps", "feature native-build-deps was renamed to theia-dev"},
		{model.KindTool, "claude", ""},
	} {
		if got := RetiredExtensionHint(tc.kind, tc.name); got != tc.want {
			t.Errorf("RetiredExtensionHint(%s, %q) = %q, want %q", tc.kind, tc.name, got, tc.want)
		}
	}
}

func TestLoadProfileNamesRetiredToolReplacement(t *testing.T) {
	paths := testPathsWithExtensions(t, "testdata/spec")
	_, err := LoadProfile(paths, "mistral-vibe")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("LoadProfile error = %v, want fs.ErrNotExist", err)
	}
	if want := RetiredExtensionHint(model.KindTool, "mistral-vibe"); err.Error() != want {
		t.Fatalf("LoadProfile error = %q, want %q", err, want)
	}
}

func TestLoadProfileFindsInstalledRetiredTool(t *testing.T) {
	root := t.TempDir()
	spec := "schemaVersion: \"1\"\nkind: sandbox\nname: mistral-vibe\n"
	writeTestFile(t, filepath.Join(root, "tools", "mistral-vibe", SpecFilename), spec)
	if _, err := LoadProfile(model.Paths{ToolsDir: filepath.Join(root, "tools")}, "mistral-vibe"); err != nil {
		t.Fatalf("LoadProfile of an installed retired tool: %v", err)
	}
}
