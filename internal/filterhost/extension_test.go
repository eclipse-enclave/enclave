// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filterhost

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/testutil"
)

func testPaths(t *testing.T) model.Paths {
	t.Helper()
	root := t.TempDir()
	return model.Paths{
		UserExtensionsDir:       filepath.Join(root, "extensions"),
		UserFilterExtensionsDir: filepath.Join(root, "extensions", "filters"),
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestListAndLoadFilters(t *testing.T) {
	paths := testPaths(t)
	testutil.InstallGWSFilter(t, paths.UserFilterExtensionsDir)

	exts, err := List(paths, func(msg string) { t.Errorf("warning: %s", msg) })
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(exts) != 1 || exts[0].Name != "gws" || exts[0].Type != model.ExtensionKindFilter {
		t.Fatalf("exts = %+v", exts)
	}
	f, err := Load(paths, "gws")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.DefaultPreset != "readonly" || !slices.Contains(f.Hosts, "gmail.googleapis.com") {
		t.Fatalf("filter = %+v", f)
	}
	if _, err := Load(paths, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(missing) = %v, want not exist", err)
	}
	if _, err := Load(paths, "../gws"); err == nil {
		t.Fatal("Load accepted a path as a name")
	}

	validation, err := ValidateInstalled(paths)
	if err != nil {
		t.Fatalf("ValidateInstalled: %v", err)
	}
	if len(validation.Errors) != 0 || len(validation.Warnings) != 0 {
		t.Fatalf("valid filter reported %v / %v", validation.Errors, validation.Warnings)
	}
}

func TestListSkipsBrokenFilter(t *testing.T) {
	paths := testPaths(t)
	writeFile(t, filepath.Join(paths.UserFilterExtensionsDir, "bad", "spec.yaml"), "schemaVersion: \"1\"\nkind: filter\nname: bad\n")
	var warnings []string
	exts, err := List(paths, func(msg string) { warnings = append(warnings, msg) })
	if err != nil || len(exts) != 0 || len(warnings) != 1 || !strings.HasPrefix(warnings[0], "Skipping filter bad: ") {
		t.Fatalf("List = %+v, %v, warnings %v", exts, err, warnings)
	}
}

func TestValidateDirReportsIgnoredFiles(t *testing.T) {
	paths := testPaths(t)
	dir := testutil.InstallGWSFilter(t, paths.UserFilterExtensionsDir)
	writeFile(t, filepath.Join(dir, "install.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(dir, "skills", "x", "SKILL.md"), "x\n")
	writeFile(t, filepath.Join(dir, model.CommandsDirName, "session", "x"), "#!/bin/sh\n")

	content := ValidateDir("gws", dir)
	structure := config.ValidateExtensionDir(dir, model.KindFilter, paths)
	if len(content.Errors) != 0 || len(structure.Errors) != 0 {
		t.Fatalf("errors = %v %v", content.Errors, structure.Errors)
	}
	joined := strings.Join(append(content.Warnings, structure.Warnings...), "\n")
	for _, want := range []string{
		`filter "gws": install.sh is ignored`,
		`filter "gws": skills is ignored`,
		`filter "gws": ignoring commands/session`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings do not contain %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, dir) {
		t.Errorf("warnings name the absolute directory:\n%s", joined)
	}
}

func TestValidateDirErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(spec string) string
		want string
	}{
		{"unknown field", func(s string) string { return s + "bogus: 1\n" }, "invalid spec.yaml"},
		{"reserved env", func(s string) string {
			return strings.Replace(s, "env: [GOOGLE_WORKSPACE_CLI_TOKEN]", "env: [ENCLAVE_X]", 1)
		}, "reserved ENCLAVE_ prefix"},
		{"wrong name", func(s string) string { return strings.Replace(s, "name: gws", "name: other", 1) }, `name must be "gws"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := testutil.InstallGWSFilter(t, t.TempDir())
			specPath := filepath.Join(dir, config.SpecFilename)
			data, err := os.ReadFile(specPath) // #nosec G304 -- test fixture.
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, specPath, tc.edit(string(data)))

			validation := ValidateDir("gws", dir)
			if len(validation.Errors) != 1 || !strings.Contains(validation.Errors[0], tc.want) {
				t.Fatalf("errors = %v, want one containing %q", validation.Errors, tc.want)
			}
			if strings.Contains(validation.Errors[0], dir) {
				t.Errorf("error names the absolute directory: %s", validation.Errors[0])
			}
		})
	}
}

func TestSummarizeSpecDirReadsFilterMetadata(t *testing.T) {
	dir := testutil.InstallGWSFilter(t, t.TempDir())
	summary, err := config.SummarizeSpecDir(dir)
	if err != nil {
		t.Fatalf("SummarizeSpecDir: %v", err)
	}
	if summary.Kind != config.KindFilter || summary.Name != "gws" || summary.DisplayName == "" {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestFilterExtensionRoots(t *testing.T) {
	paths := testPaths(t)
	builtin, user := config.ExtensionRoots(paths, model.KindFilter)
	if builtin != "" || user != paths.UserFilterExtensionsDir {
		t.Fatalf("ExtensionRoots = %q, %q", builtin, user)
	}
}
