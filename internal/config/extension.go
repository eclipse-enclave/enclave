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
	"sort"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

// ResolveToolFile resolves a tool extension file path with user override support.
func ResolveToolFile(paths model.Paths, toolName string, fileName string) (string, bool) {
	for _, candidate := range toolFileCandidates(paths, toolName, fileName) {
		if util.FileExists(candidate) {
			return candidate, true
		}
	}
	return "", false
}

// toolFileCandidates lists the paths ResolveToolFile searches, in its
// precedence order: the user extension tree ahead of the built-in one.
func toolFileCandidates(paths model.Paths, toolName string, fileName string) []string {
	candidates := make([]string, 0, 2)
	for _, toolsRoot := range []string{paths.UserToolsDir, paths.ToolsDir} {
		if strings.TrimSpace(toolsRoot) == "" {
			continue
		}
		candidates = append(candidates, filepath.Join(toolsRoot, toolName, fileName))
	}
	return candidates
}

// toolSettingsTemplateName returns the bare template filename a tool's
// settings_file refers to. Consumers join that name onto a templates directory
// on the host and inside the container, so a missing name or an embedded path
// separator is rejected here rather than escaping the templates directory.
// validateAndNormalizeProfile applies this at spec load, which is what lets the
// other consumers use the raw settings_file.
func toolSettingsTemplateName(toolName string, settingsFile string) (string, error) {
	prefix := toolName + "-"
	if !strings.HasPrefix(settingsFile, prefix) {
		return "", fmt.Errorf("settings_file %q must start with %q", settingsFile, prefix)
	}
	templateName := strings.TrimPrefix(settingsFile, prefix)
	if templateName == "" {
		return "", fmt.Errorf("settings_file %q is missing a template name after %q", settingsFile, prefix)
	}
	if strings.ContainsAny(templateName, `/\`) {
		return "", fmt.Errorf("settings_file %q must not contain a path separator", settingsFile)
	}
	return templateName, nil
}

// ResolveToolSettingsTemplate resolves the settings template named by a tool's
// settings_file. The template lives under templates/ in the extension tree, and
// a user-global extension is not staged into the built-in assets tree, so both
// trees are searched. Runtime and `validate-extensions` share this so a spec
// cannot validate but then fail at session start.
func ResolveToolSettingsTemplate(paths model.Paths, toolName string, settingsFile string) (string, error) {
	templateName, err := toolSettingsTemplateName(toolName, settingsFile)
	if err != nil {
		return "", err
	}
	relativePath := filepath.Join(model.TemplatesDir, templateName)
	templatePath, ok := ResolveToolFile(paths, toolName, relativePath)
	if !ok {
		return "", fmt.Errorf("missing template %q, searched: %s",
			templateName, strings.Join(toolFileCandidates(paths, toolName, relativePath), ", "))
	}
	return templatePath, nil
}

// ResolveFeatureFile resolves a feature extension file path with user override support.
func ResolveFeatureFile(paths model.Paths, featureName string, fileName string) (string, bool) {
	if paths.UserFeaturesDir != "" {
		candidate := filepath.Join(paths.UserFeaturesDir, featureName, fileName)
		if util.FileExists(candidate) {
			return candidate, true
		}
	}
	candidate := filepath.Join(paths.FeaturesDir, featureName, fileName)
	if util.FileExists(candidate) {
		return candidate, true
	}
	return "", false
}

// ResolveToolDirs returns the built-in and user tool extension directories.
func ResolveToolDirs(paths model.Paths, toolName string) (builtinDir string, userDir string) {
	builtin := filepath.Join(paths.ToolsDir, toolName)
	if util.IsDir(builtin) {
		builtinDir = builtin
	}
	if paths.UserToolsDir != "" {
		candidate := filepath.Join(paths.UserToolsDir, toolName)
		if util.IsDir(candidate) {
			userDir = candidate
		}
	}
	return builtinDir, userDir
}

// ResolveFeatureDirs returns the built-in and user feature extension directories.
func ResolveFeatureDirs(paths model.Paths, featureName string) (builtinDir string, userDir string) {
	builtin := filepath.Join(paths.FeaturesDir, featureName)
	if util.IsDir(builtin) {
		builtinDir = builtin
	}
	if paths.UserFeaturesDir != "" {
		candidate := filepath.Join(paths.UserFeaturesDir, featureName)
		if util.IsDir(candidate) {
			userDir = candidate
		}
	}
	return builtinDir, userDir
}

// ResolveToolSubdirs returns the existing subdir directories of a tool
// extension, built-in tree first so the user extension tree overlays it.
func ResolveToolSubdirs(paths model.Paths, toolName string, subdir string) []string {
	builtinDir, userDir := ResolveToolDirs(paths, toolName)
	return existingSubdirs([]string{builtinDir, userDir}, subdir)
}

// ResolveFeatureSubdirs returns the existing subdir directories of a feature
// extension, built-in tree first so the user extension tree overlays it.
func ResolveFeatureSubdirs(paths model.Paths, featureName string, subdir string) []string {
	builtinDir, userDir := ResolveFeatureDirs(paths, featureName)
	return existingSubdirs([]string{builtinDir, userDir}, subdir)
}

func existingSubdirs(extensionDirs []string, subdir string) []string {
	dirs := make([]string, 0, len(extensionDirs))
	for _, extensionDir := range extensionDirs {
		if extensionDir == "" {
			continue
		}
		candidate := filepath.Join(extensionDir, subdir)
		if util.IsDir(candidate) {
			dirs = append(dirs, candidate)
		}
	}
	return dirs
}

// LoadToolExtension loads a tool extension from its spec.yaml/spec.json
// document. It returns os.ErrNotExist (via LoadSpec) if no spec is present.
func LoadToolExtension(paths model.Paths, name string) (model.Extension, error) {
	return loadSpecExtension(paths, name, KindSandbox, model.ExtensionKindSandbox)
}

// LoadFeatureExtension loads a feature extension from its spec.yaml/spec.json
// document. It returns os.ErrNotExist (via LoadSpec) if no spec is present.
func LoadFeatureExtension(paths model.Paths, name string) (model.Extension, error) {
	return loadSpecExtension(paths, name, KindMixin, model.ExtensionKindMixin)
}

// ListTools returns all tool extension names from both built-in and user extension roots.
func ListTools(paths model.Paths) ([]string, error) {
	return listSpecNames(paths, KindSandbox)
}

// listSpecNames returns the sorted names of the given kind's extensions, from
// both the built-in and the user root, that carry a spec document. Unlike
// ListFeatures it never loads those specs, so a name it returns may still fail
// to load.
func listSpecNames(paths model.Paths, kind string) ([]string, error) {
	var builtinDir, userDir string
	switch kind {
	case KindSandbox:
		builtinDir, userDir = paths.ToolsDir, paths.UserToolsDir
	case KindMixin:
		builtinDir, userDir = paths.FeaturesDir, paths.UserFeaturesDir
	default:
		return nil, fmt.Errorf("unknown extension kind %q", kind)
	}

	names, err := listExtensionNames(builtinDir, userDir)
	if err != nil {
		return nil, err
	}

	var withSpec []string
	for _, name := range names {
		if hasSpecFile(paths, name, kind) {
			withSpec = append(withSpec, name)
		}
	}
	return withSpec, nil
}

// ListFeatures returns all feature extensions from both built-in and user roots, sorted by priority.
func ListFeatures(paths model.Paths) ([]model.Extension, error) {
	names, err := listSpecNames(paths, KindMixin)
	if err != nil {
		return nil, err
	}

	var features []model.Extension
	for _, name := range names {
		ext, err := LoadFeatureExtension(paths, name)
		if err != nil {
			// A feature dropped here is silently missing from the built image,
			// so surface why. Spec-less directories are already filtered out.
			specWarn(fmt.Sprintf("feature %q: %v; skipping", name, err))
			continue
		}
		features = append(features, ext)
	}

	// Sort by priority (lower priority value = earlier)
	sort.Slice(features, func(i, j int) bool {
		if features[i].Priority == features[j].Priority {
			return features[i].Name < features[j].Name
		}
		return features[i].Priority < features[j].Priority
	})

	return features, nil
}

func listExtensionNames(primaryDir string, secondaryDir string) ([]string, error) {
	names := map[string]struct{}{}
	if err := appendExtensionNames(primaryDir, names); err != nil {
		return nil, err
	}
	if secondaryDir != "" {
		if err := appendExtensionNames(secondaryDir, names); err != nil {
			return nil, err
		}
	}
	merged := make([]string, 0, len(names))
	for name := range names {
		merged = append(merged, name)
	}
	sort.Strings(merged)
	return merged, nil
}

func appendExtensionNames(dir string, names map[string]struct{}) error {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
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
		names[entry.Name()] = struct{}{}
	}
	return nil
}

// extensionManifestState records which optional extension-level fields were
// explicitly present in the on-disk spec document, so applyExtensionDefaults
// can fill in kind-specific defaults only for the fields the author omitted.
type extensionManifestState struct {
	PrioritySet        bool
	DefaultEnabledSet  bool
	DefaultIncludedSet bool
}

func applyExtensionDefaults(ext model.Extension, name string, defaultType string, state extensionManifestState) model.Extension {
	if ext.Name == "" {
		ext.Name = name
	}
	if ext.Type == "" {
		ext.Type = defaultType
	}
	if !state.PrioritySet {
		ext.Priority = model.DefaultExtensionPriority
	}
	switch defaultType {
	case model.ExtensionKindMixin:
		if !state.DefaultEnabledSet {
			ext.DefaultEnabled = true
		}
	case model.ExtensionKindSandbox:
		if !state.DefaultIncludedSet {
			ext.DefaultIncluded = true
		}
	}
	return ext
}

func validateExtensionIdentity(ext model.Extension, name string, expectedType string, manifestPath string) error {
	if ext.Type != expectedType {
		return fmt.Errorf("%s type must be %q", manifestPath, expectedType)
	}
	if ext.Name != name {
		return fmt.Errorf("%s name must be %q", manifestPath, name)
	}
	return nil
}

func validateAndNormalizeExtension(ext *model.Extension, manifestPath string) error {
	if ext == nil {
		return fmt.Errorf("%s: extension is nil", manifestPath)
	}
	normalizedSecrets, err := validateAndNormalizeSecretConfigs(ext.Secrets)
	if err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	ext.Secrets = normalizedSecrets
	if err := normalizePortConfigs(ext.Ports, ext.Name); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	return nil
}
