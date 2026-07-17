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

	"enclave/internal/model"
	"enclave/internal/util"
)

// ResolveToolFile resolves a tool extension file path with user override support.
func ResolveToolFile(paths model.Paths, toolName string, fileName string) (string, bool) {
	if paths.UserToolsDir != "" {
		candidate := filepath.Join(paths.UserToolsDir, toolName, fileName)
		if util.FileExists(candidate) {
			return candidate, true
		}
	}
	candidate := filepath.Join(paths.ToolsDir, toolName, fileName)
	if util.FileExists(candidate) {
		return candidate, true
	}
	return "", false
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
	if isDir(builtin) {
		builtinDir = builtin
	}
	if paths.UserToolsDir != "" {
		candidate := filepath.Join(paths.UserToolsDir, toolName)
		if isDir(candidate) {
			userDir = candidate
		}
	}
	return builtinDir, userDir
}

// ResolveFeatureDirs returns the built-in and user feature extension directories.
func ResolveFeatureDirs(paths model.Paths, featureName string) (builtinDir string, userDir string) {
	builtin := filepath.Join(paths.FeaturesDir, featureName)
	if isDir(builtin) {
		builtinDir = builtin
	}
	if paths.UserFeaturesDir != "" {
		candidate := filepath.Join(paths.UserFeaturesDir, featureName)
		if isDir(candidate) {
			userDir = candidate
		}
	}
	return builtinDir, userDir
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
	names, err := listExtensionNames(paths.ToolsDir, paths.UserToolsDir)
	if err != nil {
		return nil, err
	}

	var tools []string
	for _, name := range names {
		if hasSpecFile(paths, name, KindSandbox) {
			tools = append(tools, name)
		}
	}

	sort.Strings(tools)
	return tools, nil
}

// ListFeatures returns all feature extensions from both built-in and user roots, sorted by priority.
func ListFeatures(paths model.Paths) ([]model.Extension, error) {
	names, err := listExtensionNames(paths.FeaturesDir, paths.UserFeaturesDir)
	if err != nil {
		return nil, err
	}

	var features []model.Extension
	for _, name := range names {
		ext, err := LoadFeatureExtension(paths, name)
		if err != nil {
			continue // Skip invalid extensions
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

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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
	return nil
}
