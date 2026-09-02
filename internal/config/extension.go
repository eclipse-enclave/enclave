// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

// extensionNamePattern is the charset an extension name must match. An
// extension name is not just a directory name: it is interpolated into the
// generated Dockerfile as a build-context path, a COPY target, a build stage
// name, and a FEATURES environment assignment inside a shell-form RUN. Anything
// outside this set could carry shell metacharacters or a newline into those
// positions, so the charset is the boundary rather than escaping at each sink.
var extensionNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ExtensionNameCharset describes extensionNamePattern in user-facing words, for
// callers that identify the offending extension by other means and so must not
// repeat its name in the message.
const ExtensionNameCharset = "must start with a lowercase letter or digit and use only lowercase letters, digits, \".\", \"-\", and \"_\""

// ValidateExtensionName reports whether name is usable as an extension's
// identity.
func ValidateExtensionName(name string) error {
	if name == "" {
		return fmt.Errorf("extension name must not be empty")
	}
	if !extensionNamePattern.MatchString(name) {
		return fmt.Errorf("invalid extension name %q: %s", name, ExtensionNameCharset)
	}
	return nil
}

// ExtensionRoots returns the built-in and user extension roots holding
// extensions of kind. The user root is empty when no host config root could be
// resolved.
func ExtensionRoots(paths model.Paths, kind model.ExtensionKind) (builtinRoot string, userRoot string) {
	if kind == model.KindTool {
		return paths.ToolsDir, paths.UserToolsDir
	}
	return paths.FeaturesDir, paths.UserFeaturesDir
}

// withUserExtensionRoot points the user extension root of kind at root,
// leaving every other path untouched.
func withUserExtensionRoot(paths model.Paths, kind model.ExtensionKind, root string) model.Paths {
	if kind == model.KindTool {
		paths.UserToolsDir = root
		return paths
	}
	paths.UserFeaturesDir = root
	return paths
}

// ResolveExtensionFile resolves a file inside an extension of kind, preferring
// the user extension over the built-in of the same name.
func ResolveExtensionFile(paths model.Paths, kind model.ExtensionKind, name string, fileName string) (string, bool) {
	for _, candidate := range extensionFileCandidates(paths, kind, name, fileName) {
		if util.FileExists(candidate) {
			return candidate, true
		}
	}
	return "", false
}

// extensionFileCandidates lists the paths ResolveExtensionFile searches, in its
// precedence order: the user extension tree ahead of the built-in one.
func extensionFileCandidates(paths model.Paths, kind model.ExtensionKind, name string, fileName string) []string {
	builtinRoot, userRoot := ExtensionRoots(paths, kind)
	candidates := make([]string, 0, 2)
	for _, root := range []string{userRoot, builtinRoot} {
		if strings.TrimSpace(root) == "" {
			continue
		}
		candidates = append(candidates, filepath.Join(root, name, fileName))
	}
	return candidates
}

// ResolveToolFile resolves a tool extension file path with user override support.
func ResolveToolFile(paths model.Paths, toolName string, fileName string) (string, bool) {
	return ResolveExtensionFile(paths, model.KindTool, toolName, fileName)
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
		return "", fmt.Errorf("missing template %q, searched: %s", templateName,
			strings.Join(extensionFileCandidates(paths, model.KindTool, toolName, relativePath), ", "))
	}
	return templatePath, nil
}

// ResolveUpdateProbe resolves the check-update.sh script the automatic update
// check runs for an extension. It searches the tool roots only.
func ResolveUpdateProbe(paths model.Paths, name string) (string, bool) {
	return ResolveToolFile(paths, name, model.CheckUpdateScriptFilename)
}

// UpdateProbeApplies reports whether check-update.sh is ever run for an
// extension of specKind (model.ExtensionKindSandbox or
// model.ExtensionKindMixin). Only sandbox extensions have an automatic update
// check, since ResolveUpdateProbe searches the tool roots alone.
func UpdateProbeApplies(specKind string) bool {
	return specKind == model.ExtensionKindSandbox
}

// ResolveFeatureFile resolves a feature extension file path with user override support.
func ResolveFeatureFile(paths model.Paths, featureName string, fileName string) (string, bool) {
	return ResolveExtensionFile(paths, model.KindFeature, featureName, fileName)
}

// ResolveExtensionDirs returns the directories one extension of kind occupies:
// its built-in directory and its user directory, each empty when it does not
// exist. SourceLabel classifies the result.
func ResolveExtensionDirs(paths model.Paths, kind model.ExtensionKind, name string) (builtinDir string, userDir string) {
	builtinRoot, userRoot := ExtensionRoots(paths, kind)
	if builtin := filepath.Join(builtinRoot, name); util.IsDir(builtin) {
		builtinDir = builtin
	}
	if userRoot != "" {
		if candidate := filepath.Join(userRoot, name); util.IsDir(candidate) {
			userDir = candidate
		}
	}
	return builtinDir, userDir
}

// ResolveToolDirs returns the built-in and user tool extension directories.
func ResolveToolDirs(paths model.Paths, toolName string) (builtinDir string, userDir string) {
	return ResolveExtensionDirs(paths, model.KindTool, toolName)
}

// ResolveFeatureDirs returns the built-in and user feature extension directories.
func ResolveFeatureDirs(paths model.Paths, featureName string) (builtinDir string, userDir string) {
	return ResolveExtensionDirs(paths, model.KindFeature, featureName)
}

const (
	SourceBuiltin  = "builtin"
	SourceUser     = "user"
	SourceOverride = "override"
)

// SourceLabel classifies an extension's origin from its resolved built-in and
// user directories (as returned by ResolveExtensionDirs).
func SourceLabel(builtinDir string, userDir string) string {
	switch {
	case builtinDir != "" && userDir != "":
		return SourceOverride
	case userDir != "":
		return SourceUser
	default:
		return SourceBuiltin
	}
}

// LoadExtension loads an extension of kind from its spec.yaml/spec.json
// document. It returns os.ErrNotExist (via LoadSpec) if no spec is present.
func LoadExtension(paths model.Paths, kind model.ExtensionKind, name string) (model.Extension, error) {
	return loadSpecExtension(paths, name, kind.SpecKind())
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
	return LoadExtension(paths, model.KindTool, name)
}

// LoadFeatureExtension loads a feature extension from its spec.yaml/spec.json
// document. It returns os.ErrNotExist (via LoadSpec) if no spec is present.
func LoadFeatureExtension(paths model.Paths, name string) (model.Extension, error) {
	return LoadExtension(paths, model.KindFeature, name)
}

// ListExtensionDirNames returns the name of every extension directory of kind
// under either root, whether or not it holds a loadable spec. This is the
// enumeration a management command needs: an extension whose spec stopped
// parsing — an upstream schemaVersion bump is enough — still occupies a
// directory, and remove is the recovery path for it.
func ListExtensionDirNames(paths model.Paths, kind model.ExtensionKind) ([]string, error) {
	return listExtensionDirNames(ExtensionRoots(paths, kind))
}

// ListTools returns all tool extension names from both built-in and user extension roots.
func ListTools(paths model.Paths) ([]string, error) {
	names, err := listExtensionDirNames(ExtensionRoots(paths, model.KindTool))
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
	names, err := listExtensionDirNames(ExtensionRoots(paths, model.KindFeature))
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

// listExtensionDirNames merges the extension directory names found in two
// roots, sorted and deduplicated.
func listExtensionDirNames(primaryDir string, secondaryDir string) ([]string, error) {
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
		if !isExtensionDir(entry) {
			continue
		}
		names[entry.Name()] = struct{}{}
	}
	return nil
}

// isExtensionDir reports whether a directory entry of an extension root is an
// extension. Dot-prefixed names never are: that is what keeps the extension
// installer's own staging directories (.incoming-*, .replaced-*) and stray
// dotfiles from being listed, loaded, or validated as extensions, and
// extinstall.validExtensionName relies on it by refusing to install under a
// dot-prefixed name.
func isExtensionDir(entry fs.DirEntry) bool {
	return entry.IsDir() && !strings.HasPrefix(entry.Name(), ".")
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
