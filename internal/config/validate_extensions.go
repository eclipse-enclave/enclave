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
	if err := validateUserExtensions(paths, model.KindTool, &result); err != nil {
		return result, err
	}
	if err := validateUserExtensions(paths, model.KindFeature, &result); err != nil {
		return result, err
	}

	return result, nil
}

// ValidateExtensionDir validates the single user extension directory dir as an
// extension of kind. dir's parent stands in for the kind's user extension root,
// so built-ins still resolve from paths while nothing else is scanned: that is
// what lets the extension installer check a staged copy, before the swap,
// exactly as it would be checked once installed. No built-in pass runs beside
// it, so spec content is validated even when dir shadows a built-in.
func ValidateExtensionDir(dir string, kind model.ExtensionKind, paths model.Paths) ExtensionValidation {
	result := ExtensionValidation{}
	scoped := withUserExtensionRoot(paths, kind, filepath.Dir(dir))
	validateUserExtension(scoped, kind, filepath.Base(dir), true, &result)
	return result
}

// hasOwnSpecFile reports whether extDir itself (no user-override resolution)
// contains a spec.yaml or spec.json.
func hasOwnSpecFile(extDir string) bool {
	_, ok := ownSpecFile(extDir)
	return ok
}

func validateBuiltinToolExtensions(paths model.Paths, result *ExtensionValidation) error {
	entries, err := os.ReadDir(paths.ToolsDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !isExtensionDir(entry) {
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

// validateUserExtensions validates every extension in the user extension root
// of kind, one directory at a time.
func validateUserExtensions(paths model.Paths, kind model.ExtensionKind, result *ExtensionValidation) error {
	_, userRoot := ExtensionRoots(paths, kind)
	if strings.TrimSpace(userRoot) == "" {
		return nil
	}
	entries, err := os.ReadDir(userRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !isExtensionDir(entry) {
			continue
		}
		validateUserExtension(paths, kind, entry.Name(), false, result)
	}

	return nil
}

// validateUserExtension validates one user extension of kind. When a built-in
// of the same name exists, its spec content is validated only if
// validateOverrideContent is set: the full-tree pass leaves it off because its
// built-in pass already checked the same merged spec.
func validateUserExtension(paths model.Paths, kind model.ExtensionKind, name string, validateOverrideContent bool, result *ExtensionValidation) {
	_, userRoot := ExtensionRoots(paths, kind)
	userDir := filepath.Join(userRoot, name)
	builtinDir, _ := ResolveExtensionDirs(paths, kind, name)
	hasBuiltin := builtinDir != ""

	if util.IsDir(filepath.Join(userDir, model.ExtensionGoDir)) {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%s %q: user go/ handlers are ignored (requires recompilation)", kind.Label(), name))
	}

	if !hasBuiltin && !hasOwnSpecFile(userDir) {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%s %q: missing %s in user extension (skipping)", kind.Label(), name, SpecFilename))
		return
	}
	switch {
	case !hasBuiltin:
		validateUserOnlyExtension(paths, kind, name, userDir, result)
	case validateOverrideContent:
		validateSpecContent(paths, kind, name, result)
	}

	if kind == model.KindTool {
		userAllowlistPath := filepath.Join(userDir, model.AllowlistFilename)
		if util.PathExists(userAllowlistPath) {
			validateUserAllowlistIncludes(paths, name, userAllowlistPath, result)
		}
	}
}

// validateUserOnlyExtension validates a user extension that shadows no
// built-in: nothing else ever looks at its spec document, and a tool
// additionally has to carry the files the build expects to find beside it.
func validateUserOnlyExtension(paths model.Paths, kind model.ExtensionKind, name string, userDir string, result *ExtensionValidation) {
	validateSpecContent(paths, kind, name, result)
	if kind != model.KindTool {
		return
	}
	for _, required := range []string{model.InstallScriptFilename, model.AllowlistFilename} {
		if !util.PathExists(filepath.Join(userDir, required)) {
			result.Warnings = append(result.Warnings, fmt.Sprintf("tool %q: missing %s in user extension", name, required))
		}
	}
}

// validateSpecContent validates the effective (override-resolved) spec document
// for name.
func validateSpecContent(paths model.Paths, kind model.ExtensionKind, name string, result *ExtensionValidation) {
	switch kind {
	case model.KindTool:
		validateToolSpecContent(paths, name, result)
	default:
		if _, err := LoadExtension(paths, kind, name); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s %q: invalid %s: %v", kind.Label(), name, SpecFilename, err))
		}
	}
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
	if _, err := ResolveToolSettingsTemplate(paths, toolName, settingsFile); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("tool %q: %v", toolName, err))
	}
}

func validateBuiltinFeatureExtensions(paths model.Paths, result *ExtensionValidation) error {
	entries, err := os.ReadDir(paths.FeaturesDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !isExtensionDir(entry) {
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
