// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"enclave/internal/extinstall"
	"enclave/internal/model"
)

func commandExtensionContext(t *testing.T) *AppContext {
	t.Helper()
	tmp := t.TempDir()
	root := filepath.Join(tmp, "user", "extensions", "commands")
	dir := filepath.Join(root, "usage")
	writeAppFile(t, filepath.Join(dir, "spec.yaml"), "schemaVersion: \"1\"\nkind: command\nname: usage\ndisplayName: Usage\n", 0o644)
	writeAppFile(t, filepath.Join(dir, "commands", "host", "usage"), "#!/bin/sh\n", 0o755)
	return NewAppContext(model.Paths{UserCommandExtensionsDir: root}, tmp)
}

func TestRunCommandExtensionsListsWithHostCommands(t *testing.T) {
	ctx := commandExtensionContext(t)

	text := captureStdout(t, func() {
		if code := runCommandExtensions(ctx, nil); code != 0 {
			t.Errorf("exit code = %d", code)
		}
	})
	if !strings.Contains(text, "✓ usage [host: usage]") {
		t.Fatalf("listing = %q", text)
	}

	out := captureStdout(t, func() {
		if code := runCommandExtensions(ctx, &extinstall.Request{Kind: model.KindCommand, JSON: true}); code != 0 {
			t.Errorf("exit code = %d", code)
		}
	})
	var decoded struct {
		SchemaVersion string `json:"schemaVersion"`
		Kind          string `json:"kind"`
		Extensions    []struct {
			Name         string   `json:"name"`
			DisplayName  string   `json:"displayName"`
			Source       string   `json:"source"`
			Enabled      bool     `json:"enabled"`
			HostCommands []string `json:"hostCommands"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if decoded.SchemaVersion != "1" || decoded.Kind != "command" || len(decoded.Extensions) != 1 {
		t.Fatalf("envelope = %+v", decoded)
	}
	ext := decoded.Extensions[0]
	if ext.Name != "usage" || ext.DisplayName != "Usage" || ext.Source != "user" || !ext.Enabled ||
		!slices.Equal(ext.HostCommands, []string{"usage"}) {
		t.Fatalf("entry = %+v", ext)
	}
}

func TestRunCommandExtensionsWithNoneInstalled(t *testing.T) {
	ctx := NewAppContext(model.Paths{UserCommandExtensionsDir: filepath.Join(t.TempDir(), "missing")}, t.TempDir())
	text := captureStdout(t, func() { runCommandExtensions(ctx, nil) })
	if !strings.Contains(text, "No command extensions installed") {
		t.Fatalf("listing = %q", text)
	}
}
