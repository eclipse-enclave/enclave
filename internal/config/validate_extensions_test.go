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

func TestValidateExtensionsUserAllowlistInvalidIncludeErrors(t *testing.T) {
	tmp := t.TempDir()
	paths := testValidationPaths(tmp)
	ensureValidationDirs(t, paths)
	createValidToolExtension(t, paths.ToolsDir, "claude")
	writeValidationFile(t, filepath.Join(paths.UserToolsDir, "claude", model.AllowlistFilename), "conf-file=/tmp/not-allowed.conf\n")

	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	if !containsText(validation.Errors, "invalid conf-file include") {
		t.Fatalf("expected invalid include error, got errors=%v", validation.Errors)
	}
}

func TestValidateExtensionsUserAllowlistBuiltInIncludeAccepted(t *testing.T) {
	tmp := t.TempDir()
	paths := testValidationPaths(tmp)
	ensureValidationDirs(t, paths)
	createValidToolExtension(t, paths.ToolsDir, "claude")
	writeValidationFile(t, filepath.Join(paths.AllowlistsDir, "fragments", "base.conf"), "server=/example.com/8.8.8.8\n")
	writeValidationFile(t, filepath.Join(paths.UserToolsDir, "claude", model.AllowlistFilename), "conf-file=/etc/dnsmasq.allowlists/fragments/base.conf\n")

	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	if containsText(validation.Errors, "invalid conf-file include") {
		t.Fatalf("expected valid include, got errors=%v", validation.Errors)
	}
}

func TestValidateExtensionsUserGoDirWarnsAndPartialOverrideNeedsNoManifest(t *testing.T) {
	tmp := t.TempDir()
	paths := testValidationPaths(tmp)
	ensureValidationDirs(t, paths)
	createValidToolExtension(t, paths.ToolsDir, "claude")
	writeValidationFile(t, filepath.Join(paths.UserToolsDir, "claude", "go", "README.md"), "ignored\n")
	writeValidationFile(t, filepath.Join(paths.UserToolsDir, "claude", "templates", "custom.json"), "{}\n")

	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	if !containsText(validation.Warnings, "go/ handlers are ignored") {
		t.Fatalf("expected go/ warning, got warnings=%v", validation.Warnings)
	}
	if containsText(validation.Warnings, "missing spec.yaml in user extension") {
		t.Fatalf("partial override should not warn about missing spec.yaml, warnings=%v", validation.Warnings)
	}
	if len(validation.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", validation.Errors)
	}
}

func TestValidateExtensionsUserOnlyToolMissingSpecWarnsAndSkips(t *testing.T) {
	tmp := t.TempDir()
	paths := testValidationPaths(tmp)
	ensureValidationDirs(t, paths)
	// A user-only tool directory that exists but never got a spec.yaml (for
	// example, a leftover README from a partial/abandoned override) should be
	// skipped with a warning rather than validated.
	writeValidationFile(t, filepath.Join(paths.UserToolsDir, "my-tool", "README.md"), "not a spec\n")

	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	if !containsText(validation.Warnings, "tool \"my-tool\": missing spec.yaml in user extension (skipping)") {
		t.Fatalf("expected missing spec warning, got warnings=%v", validation.Warnings)
	}
	if len(validation.Errors) != 0 {
		t.Fatalf("expected no errors, got %v", validation.Errors)
	}
}

func TestValidateExtensionsBuiltinToolMissingSpecErrors(t *testing.T) {
	tmp := t.TempDir()
	paths := testValidationPaths(tmp)
	ensureValidationDirs(t, paths)
	// A builtin tool directory with supporting files but no spec.yaml at all.
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "broken", model.InstallScriptFilename), "#!/usr/bin/env bash\n")
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "broken", model.AllowlistFilename), "server=/example.com/8.8.8.8\n")

	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	if !containsText(validation.Errors, "tool \"broken\": missing spec.yaml") {
		t.Fatalf("expected missing spec.yaml error, got errors=%v", validation.Errors)
	}
}

func TestValidateExtensionsBuiltinToolBadIdentityErrors(t *testing.T) {
	tmp := t.TempDir()
	paths := testValidationPaths(tmp)
	ensureValidationDirs(t, paths)
	// spec.yaml's name field does not match its directory name.
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "claude", SpecFilename), toolSpecYAML("wrong-name"))
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "claude", model.InstallScriptFilename), "#!/usr/bin/env bash\n")
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "claude", model.AllowlistFilename), "server=/example.com/8.8.8.8\n")

	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	if !containsText(validation.Errors, "invalid spec.yaml") {
		t.Fatalf("expected invalid spec.yaml identity error, got errors=%v", validation.Errors)
	}
}

func TestValidateExtensionsBuiltinToolBadIdentityReportedOnce(t *testing.T) {
	tmp := t.TempDir()
	paths := testValidationPaths(tmp)
	ensureValidationDirs(t, paths)
	// A tool whose spec.yaml name mismatches its directory fails to load as both
	// a model.Extension and a model.Profile — both call LoadSpec, which surfaces
	// the same identity error. It must be reported exactly once, not per load.
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "claude", SpecFilename), toolSpecYAML("wrong-name"))
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "claude", model.InstallScriptFilename), "#!/usr/bin/env bash\n")
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "claude", model.AllowlistFilename), "server=/example.com/8.8.8.8\n")

	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	invalidCount := 0
	for _, e := range validation.Errors {
		if strings.Contains(e, "invalid spec.yaml") {
			invalidCount++
		}
	}
	if invalidCount != 1 {
		t.Fatalf("expected identity mismatch reported exactly once, got %d: %v", invalidCount, validation.Errors)
	}
}

func TestValidateExtensionsBuiltinToolBadSettingsErrors(t *testing.T) {
	tmp := t.TempDir()
	paths := testValidationPaths(tmp)
	ensureValidationDirs(t, paths)
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "claude", SpecFilename), `
schemaVersion: "1"
kind: sandbox
name: claude
sandbox:
  entrypoint: { run: [claude] }
  settingsFile: claude-settings.json
`)
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "claude", model.InstallScriptFilename), "#!/usr/bin/env bash\n")
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "claude", model.AllowlistFilename), "server=/example.com/8.8.8.8\n")

	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	if !containsText(validation.Errors, "settings_target set without settings_file") &&
		!containsText(validation.Errors, "settings_file set without settings_target") {
		t.Fatalf("expected settings_file/settings_target mismatch error, got errors=%v", validation.Errors)
	}
}

func TestValidateExtensionsBuiltinToolMissingSettingsTemplateErrors(t *testing.T) {
	tmp := t.TempDir()
	paths := testValidationPaths(tmp)
	ensureValidationDirs(t, paths)
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "claude", SpecFilename), `
schemaVersion: "1"
kind: sandbox
name: claude
sandbox:
  entrypoint: { run: [claude] }
  settingsFile: claude-settings.json
  settingsTarget: .claude/settings.json
`)
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "claude", model.InstallScriptFilename), "#!/usr/bin/env bash\n")
	writeValidationFile(t, filepath.Join(paths.ToolsDir, "claude", model.AllowlistFilename), "server=/example.com/8.8.8.8\n")

	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	if !containsText(validation.Errors, "missing template") {
		t.Fatalf("expected missing template error, got errors=%v", validation.Errors)
	}
}

func TestValidateExtensionsBuiltinFeatureMissingSpecErrors(t *testing.T) {
	tmp := t.TempDir()
	paths := testValidationPaths(tmp)
	ensureValidationDirs(t, paths)
	writeValidationFile(t, filepath.Join(paths.FeaturesDir, "broken-feature", model.InstallScriptFilename), "#!/usr/bin/env bash\n")

	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	if !containsText(validation.Errors, "feature \"broken-feature\": missing spec.yaml") {
		t.Fatalf("expected missing spec.yaml error, got errors=%v", validation.Errors)
	}
}

func TestValidateExtensionsBuiltinFeatureBadIdentityErrors(t *testing.T) {
	tmp := t.TempDir()
	paths := testValidationPaths(tmp)
	ensureValidationDirs(t, paths)
	writeValidationFile(t, filepath.Join(paths.FeaturesDir, "devtools", SpecFilename), `
schemaVersion: "1"
kind: mixin
name: wrong-name
`)

	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	if !containsText(validation.Errors, "feature \"devtools\": invalid spec.yaml") {
		t.Fatalf("expected invalid spec.yaml identity error, got errors=%v", validation.Errors)
	}
}

// TestValidateExtensionsRealTreeHasZeroErrors ensures every built-in tool and
// feature manifest validates cleanly.
func TestValidateExtensionsRealTreeHasZeroErrors(t *testing.T) {
	paths := realRepoPaths(t)

	validation, err := ValidateExtensions(paths)
	if err != nil {
		t.Fatalf("ValidateExtensions: %v", err)
	}
	if len(validation.Errors) != 0 {
		t.Fatalf("expected zero errors validating the real extensions/ tree, got: %v", validation.Errors)
	}
}

func testValidationPaths(root string) model.Paths {
	return model.Paths{
		ToolsDir:          filepath.Join(root, "extensions", "tools"),
		FeaturesDir:       filepath.Join(root, "extensions", "features"),
		UserToolsDir:      filepath.Join(root, ".enclave", "extensions", "tools"),
		UserFeaturesDir:   filepath.Join(root, ".enclave", "extensions", "features"),
		AllowlistsDir:     filepath.Join(root, "runtime-assets", "gateway-allowlists"),
		ExtensionsDir:     filepath.Join(root, "extensions"),
		UserExtensionsDir: filepath.Join(root, ".enclave", "extensions"),
	}
}

func ensureValidationDirs(t *testing.T, paths model.Paths) {
	t.Helper()
	dirs := []string{
		paths.ToolsDir,
		paths.FeaturesDir,
		paths.UserToolsDir,
		paths.UserFeaturesDir,
		paths.AllowlistsDir,
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
}

func toolSpecYAML(name string) string {
	return "schemaVersion: \"1\"\nkind: sandbox\nname: " + name + "\n"
}

func createValidToolExtension(t *testing.T, toolsDir string, name string) {
	t.Helper()
	writeValidationFile(t, filepath.Join(toolsDir, name, SpecFilename), toolSpecYAML(name))
	writeValidationFile(t, filepath.Join(toolsDir, name, model.InstallScriptFilename), "#!/usr/bin/env bash\n")
	writeValidationFile(t, filepath.Join(toolsDir, name, model.AllowlistFilename), "server=/example.com/8.8.8.8\n")
}

func writeValidationFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	mode := os.FileMode(0o644)
	if strings.HasSuffix(path, ".sh") {
		mode = 0o755
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func containsText(items []string, want string) bool {
	for _, item := range items {
		if strings.Contains(item, want) {
			return true
		}
	}
	return false
}
