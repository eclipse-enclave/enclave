// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

type ExtensionValidation struct {
	Warnings []string
	Errors   []string
}

// ValidateExtensions checks every built-in and user extension and returns the
// collected warnings and errors.
//
// It validates the *effective* (override-resolved) spec for each name: when a
// user overlay shadows a built-in of the same name, only the merged result is
// checked. This is deliberate — a broken built-in spec that a valid user
// overlay fully replaces is not flagged, because the merged spec is the one
// that actually runs. The trade-off is that such a shadowed-but-broken built-in
// goes unreported until the overlay is removed.
func ValidateExtensions(paths model.Paths) (ExtensionValidation, error) {
	result := ExtensionValidation{}

	if err := validateBuiltinToolExtensions(paths, &result); err != nil {
		return result, err
	}
	if err := validateBuiltinFeatureExtensions(paths, &result); err != nil {
		return result, err
	}
	if err := validateUserToolExtensions(paths, &result); err != nil {
		return result, err
	}
	if err := validateUserFeatureExtensions(paths, &result); err != nil {
		return result, err
	}

	return result, nil
}

// hasOwnSpecFile reports whether extDir itself (no user-override resolution)
// contains a spec.yaml or spec.json.
func hasOwnSpecFile(extDir string) bool {
	return util.PathExists(filepath.Join(extDir, SpecFilename)) || util.PathExists(filepath.Join(extDir, SpecFilenameJSON))
}

func validateBuiltinToolExtensions(paths model.Paths, result *ExtensionValidation) error {
	entries, err := os.ReadDir(paths.ToolsDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		extDir := filepath.Join(paths.ToolsDir, name)

		if !hasOwnSpecFile(extDir) {
			result.Errors = append(result.Errors, fmt.Sprintf("tool %q: missing %s", name, SpecFilename))
			continue
		}

		allowlistPath := filepath.Join(extDir, model.AllowlistFilename)
		if !util.PathExists(allowlistPath) {
			result.Errors = append(result.Errors, fmt.Sprintf("tool %q: missing %s", name, model.AllowlistFilename))
		}
		installPath := filepath.Join(extDir, model.InstallScriptFilename)
		if !util.PathExists(installPath) {
			result.Errors = append(result.Errors, fmt.Sprintf("tool %q: missing %s", name, model.InstallScriptFilename))
		}

		validateToolSpecContent(paths, name, result)
	}

	return nil
}

func validateUserToolExtensions(paths model.Paths, result *ExtensionValidation) error {
	if strings.TrimSpace(paths.UserToolsDir) == "" {
		return nil
	}
	entries, err := os.ReadDir(paths.UserToolsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		userDir := filepath.Join(paths.UserToolsDir, name)
		builtinDir, _ := ResolveToolDirs(paths, name)
		hasBuiltin := builtinDir != ""

		if isDir(filepath.Join(userDir, "go")) {
			result.Warnings = append(result.Warnings, fmt.Sprintf("tool %q: user go/ handlers are ignored (requires recompilation)", name))
		}

		hasUserSpec := hasOwnSpecFile(userDir)
		if !hasBuiltin && !hasUserSpec {
			result.Warnings = append(result.Warnings, fmt.Sprintf("tool %q: missing %s in user extension (skipping)", name, SpecFilename))
			continue
		}

		if !hasBuiltin {
			// User-only tool: its spec.yaml must be fully self-contained, so
			// validate identity/profile/settings content directly here (the
			// builtin pass below never sees this name).
			validateToolSpecContent(paths, name, result)
			if !util.PathExists(filepath.Join(userDir, model.InstallScriptFilename)) {
				result.Warnings = append(result.Warnings, fmt.Sprintf("tool %q: missing %s in user extension", name, model.InstallScriptFilename))
			}
			if !util.PathExists(filepath.Join(userDir, model.AllowlistFilename)) {
				result.Warnings = append(result.Warnings, fmt.Sprintf("tool %q: missing %s in user extension", name, model.AllowlistFilename))
			}
		}
		// When hasBuiltin is true, the builtin pass already validated the
		// effective (override-resolved) spec content for this name, so a
		// user override's content is not re-validated here to avoid
		// reporting the same problem twice.

		userAllowlistPath := filepath.Join(userDir, model.AllowlistFilename)
		if util.PathExists(userAllowlistPath) {
			validateUserAllowlistIncludes(paths, name, userAllowlistPath, result)
		}
	}

	return nil
}

// validateToolSpecContent loads the effective (override-resolved) spec.yaml
// for name as both a model.Extension (identity/type) and a model.Profile
// (command, secrets, settings, ...), surfacing any parse/identity/
// normalization error as a validation error, and checking the profile's
// settings template reference.
func validateToolSpecContent(paths model.Paths, name string, result *ExtensionValidation) {
	if _, err := LoadToolExtension(paths, name); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("tool %q: invalid %s: %v", name, SpecFilename, err))
		// LoadProfile re-parses the same spec via LoadSpec, so a parse/identity
		// failure here would be reported a second time. Stop after the first:
		// the profile-specific checks below only add value once the spec loads.
		return
	}

	profile, err := LoadProfile(paths, name)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("tool %q: invalid %s: %v", name, SpecFilename, err))
		return
	}
	validateProfileSettings(paths, name, profile, result)
}

func validateProfileSettings(paths model.Paths, toolName string, profile model.Profile, result *ExtensionValidation) {
	settingsFile := strings.TrimSpace(profile.SettingsFile)
	settingsTarget := strings.TrimSpace(profile.SettingsTarget)
	if settingsFile == "" && settingsTarget == "" {
		return
	}
	if settingsFile == "" && settingsTarget != "" {
		result.Errors = append(result.Errors, fmt.Sprintf("tool %q: settings_target set without settings_file", toolName))
		return
	}
	if settingsFile != "" && settingsTarget == "" {
		result.Errors = append(result.Errors, fmt.Sprintf("tool %q: settings_file set without settings_target", toolName))
		return
	}
	prefix := toolName + "-"
	if !strings.HasPrefix(settingsFile, prefix) {
		result.Errors = append(result.Errors, fmt.Sprintf("tool %q: settings_file must start with %q", toolName, prefix))
		return
	}
	templateName := strings.TrimPrefix(settingsFile, prefix)
	if templateName == "" {
		result.Errors = append(result.Errors, fmt.Sprintf("tool %q: settings_file missing template suffix", toolName))
		return
	}
	if _, ok := ResolveToolFile(paths, toolName, filepath.Join(model.TemplatesDir, templateName)); !ok {
		result.Errors = append(result.Errors, fmt.Sprintf("tool %q: missing template %q (checked merged templates)", toolName, templateName))
	}
}

func validateBuiltinFeatureExtensions(paths model.Paths, result *ExtensionValidation) error {
	entries, err := os.ReadDir(paths.FeaturesDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		extDir := filepath.Join(paths.FeaturesDir, name)

		if !hasOwnSpecFile(extDir) {
			result.Errors = append(result.Errors, fmt.Sprintf("feature %q: missing %s", name, SpecFilename))
			continue
		}

		if _, err := LoadFeatureExtension(paths, name); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("feature %q: invalid %s: %v", name, SpecFilename, err))
		}

		installPath := filepath.Join(extDir, model.InstallScriptFilename)
		if util.PathExists(installPath) {
			if info, statErr := os.Stat(installPath); statErr == nil {
				if info.Mode()&0o111 == 0 {
					result.Warnings = append(result.Warnings, fmt.Sprintf("feature %q: %s is not executable", name, model.InstallScriptFilename))
				}
			}
		}
	}

	return nil
}

func validateUserFeatureExtensions(paths model.Paths, result *ExtensionValidation) error {
	if strings.TrimSpace(paths.UserFeaturesDir) == "" {
		return nil
	}
	entries, err := os.ReadDir(paths.UserFeaturesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		userDir := filepath.Join(paths.UserFeaturesDir, name)
		builtinDir, _ := ResolveFeatureDirs(paths, name)
		hasBuiltin := builtinDir != ""

		if isDir(filepath.Join(userDir, "go")) {
			result.Warnings = append(result.Warnings, fmt.Sprintf("feature %q: user go/ handlers are ignored (requires recompilation)", name))
		}

		hasUserSpec := hasOwnSpecFile(userDir)
		if !hasBuiltin && !hasUserSpec {
			result.Warnings = append(result.Warnings, fmt.Sprintf("feature %q: missing %s in user extension (skipping)", name, SpecFilename))
			continue
		}

		if !hasBuiltin {
			// User-only feature: validate its self-contained spec content
			// (the builtin pass above never sees this name).
			if _, err := LoadFeatureExtension(paths, name); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("feature %q: invalid %s: %v", name, SpecFilename, err))
			}
		}
		// When hasBuiltin is true, the builtin pass already validated the
		// effective (override-resolved) spec content for this name.
	}

	return nil
}

func validateUserAllowlistIncludes(paths model.Paths, toolName string, allowlistPath string, result *ExtensionValidation) {
	// #nosec G304 -- allowlistPath is built from enumerated extension directories.
	data, err := os.ReadFile(allowlistPath)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("tool %q: failed to read %s: %v", toolName, allowlistPath, err))
		return
	}

	lines := strings.Split(string(data), "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "conf-file=") {
			continue
		}
		includePath := strings.TrimSpace(strings.TrimPrefix(line, "conf-file="))
		if includePath == "" {
			result.Errors = append(result.Errors, fmt.Sprintf("tool %q: empty conf-file include at %s:%d", toolName, allowlistPath, i+1))
			continue
		}
		if err := validateBuiltInAllowlistInclude(paths.AllowlistsDir, includePath); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("tool %q: invalid conf-file include %q in %s:%d: %v", toolName, includePath, allowlistPath, i+1, err))
		}
	}
}

func validateBuiltInAllowlistInclude(allowlistsDir string, includePath string) error {
	prefix := model.GatewayAllowlistsDir + "/"
	if !strings.HasPrefix(includePath, prefix) {
		return fmt.Errorf("must reference %s", model.GatewayAllowlistsDir)
	}
	relative := strings.TrimPrefix(includePath, prefix)
	cleanRelative := filepath.Clean(relative)
	if cleanRelative == "." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) || cleanRelative == ".." || filepath.IsAbs(cleanRelative) {
		return fmt.Errorf("path traversal is not allowed")
	}
	resolved := filepath.Join(allowlistsDir, cleanRelative)
	if !util.PathWithin(allowlistsDir, resolved) {
		return fmt.Errorf("resolves outside built-in allowlists")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("fragment does not exist: %s", includePath)
	}
	if info.IsDir() {
		return fmt.Errorf("fragment is a directory: %s", includePath)
	}
	return nil
}
