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
	"strings"
	"testing"

	"enclave/internal/model"
)

const usageCommandSpecYAML = "schemaVersion: \"1\"\nkind: command\nname: usage\ndisplayName: Usage\ndescription: Token usage\n"

func writeCommandExtension(t *testing.T, paths model.Paths, spec string) string {
	t.Helper()
	dir := filepath.Join(paths.UserCommandExtensionsDir, "usage")
	writeValidationFile(t, filepath.Join(dir, SpecFilename), spec)
	hostDir := filepath.Join(dir, model.CommandsDirName, model.CommandsHostDirName)
	writeValidationFile(t, filepath.Join(hostDir, "usage"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(hostDir, "usage"), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return dir
}

func TestListCommandExtensionsLoadsMetadataOnlySpec(t *testing.T) {
	paths := newValidationPaths(t)
	writeCommandExtension(t, paths, usageCommandSpecYAML)

	exts, err := ListCommandExtensions(paths)
	if err != nil {
		t.Fatalf("ListCommandExtensions: %v", err)
	}
	if len(exts) != 1 || exts[0].Name != "usage" || exts[0].Type != model.ExtensionKindCommand || exts[0].Description != "Token usage" {
		t.Fatalf("exts = %+v", exts)
	}
	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	if len(validation.Errors) != 0 || len(validation.Warnings) != 0 {
		t.Fatalf("valid command extension reported %v / %v", validation.Errors, validation.Warnings)
	}
}

// A runtime field on a command extension can never take effect, so it is a
// load error rather than the warn-and-ignore the deferred sbx fields get.
func TestCommandSpecRejectsRuntimeFields(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra string
		want  string
	}{
		{"packages", "aptPackages: [jq]\nneedsRoot: true\n", "remove aptPackages, needsRoot"},
		{"selection", "defaultEnabled: false\n", "remove defaultEnabled"},
		{"network", "network:\n  allowedDomains: [example.com]\n", "remove network"},
		{"sbx commands key", "commands:\n  install:\n    - command: echo hi\n", "ship executables in commands/host/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := newValidationPaths(t)
			writeCommandExtension(t, paths, usageCommandSpecYAML+tc.extra)

			_, err := LoadExtension(paths, model.KindCommand, "usage")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadExtension error = %v, want it to contain %q", err, tc.want)
			}
			validation, err := ValidateExtensions(paths)
			if err != nil {
				t.Fatalf("ValidateExtensions: %v", err)
			}
			if !containsText(validation.Errors, tc.want) {
				t.Fatalf("validation errors = %v, want %q", validation.Errors, tc.want)
			}
		})
	}
}

func TestValidateCommandExtensionTree(t *testing.T) {
	paths := newValidationPaths(t)
	dir := writeCommandExtension(t, paths, usageCommandSpecYAML)
	writeValidationFile(t, filepath.Join(dir, model.InstallScriptFilename), "#!/bin/sh\n")
	if err := os.Remove(filepath.Join(dir, model.CommandsDirName, model.CommandsHostDirName, "usage")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	writeValidationFile(t, filepath.Join(dir, model.CommandsDirName, model.CommandsHostDirName, "notes.txt"), "data\n")

	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	if !containsText(validation.Warnings, "ignoring install.sh") {
		t.Errorf("expected an inert install.sh warning, got %v", validation.Warnings)
	}
	if !containsText(validation.Errors, "adds no command") {
		t.Errorf("expected an error for a command extension without a command, got %v", validation.Errors)
	}
}

// Command extensions never ship built in, so name resolution must not look
// for one relative to the working directory.
func TestResolveCommandExtensionDirsHasNoBuiltin(t *testing.T) {
	paths := newValidationPaths(t)
	t.Chdir(t.TempDir())
	if err := os.Mkdir("usage", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	builtinDir, userDir := ResolveExtensionDirs(paths, model.KindCommand, "usage")
	if builtinDir != "" || userDir != "" {
		t.Fatalf("ResolveExtensionDirs = %q, %q, want nothing", builtinDir, userDir)
	}
}
