// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package theia

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"enclave/internal/config"
	"enclave/internal/model"
)

// DefaultPreferences are applied when no override sets them. The always_allow
// confirmation makes the in-IDE AI agent usable without per-tool prompts,
// matching the trust model of a enclave session.
var DefaultPreferences = map[string]any{
	"ai-features.AiEnable.enableAI":            true,
	"ai-features.agentMode.enabled":            true,
	"ai-features.chat.defaultChatAgent":        "Coder",
	"ai-features.chat.defaultToolConfirmation": "always_allow",
	"ai-features.chat.toolConfirmation": map[string]any{
		"shellExecute": "always_allow",
	},
	"ai-features.agentSettings": map[string]any{
		"Coder": map[string]any{
			"capabilityOverrides": map[string]any{
				"shell-execution": true,
			},
		},
	},
}

// LoadPreferencesForProject returns the preferences to pass on launch, or
// nothing for non-yolo sessions. It merges three sources, highest wins:
//
//  1. built-in DefaultPreferences
//  2. global:  ~/.config/enclave/tools/theia/preferences.json  (flat map)
//  3. project: ~/.config/enclave/projects/<config hash>/config.json under {"theia":{"preferences":{...}}}
//
// The config hash is the command's resolved or pinned config source. Both roots
// honor $XDG_CONFIG_HOME on Linux (ignored on macOS).
func LoadPreferencesForProject(home string, project model.Project, yoloEnabled bool) (map[string]any, error) {
	if !yoloEnabled {
		return nil, nil
	}
	merged := make(map[string]any, len(DefaultPreferences))
	for k, v := range DefaultPreferences {
		merged[k] = v
	}
	if home != "" {
		global, err := GlobalPreferences(home)
		if err != nil {
			return nil, fmt.Errorf("load global theia preferences: %w", err)
		}
		for k, v := range global {
			merged[k] = v
		}
	}
	if home != "" && project.ConfigSourceHash() != "" {
		prefs, err := ProjectPreferences(home, project)
		if err != nil {
			return nil, fmt.Errorf("load project theia preferences: %w", err)
		}
		for k, v := range prefs {
			merged[k] = v
		}
	}
	return merged, nil
}

// PrefSource identifies where an effective preference value came from.
type PrefSource string

const (
	SourceDefault PrefSource = "default"
	SourceGlobal  PrefSource = "global"
	SourceProject PrefSource = "project"
)

// EffectivePref is one resolved preference plus the layer that supplied it.
type EffectivePref struct {
	Key    string     `json:"key"`
	Value  any        `json:"value"`
	Source PrefSource `json:"source"`
}

// Effective returns the merged preference set with each value tagged by its
// source layer, sorted by key. The project must already be resolved or pinned.
func Effective(home string, project model.Project, yoloEnabled bool) ([]EffectivePref, error) {
	if !yoloEnabled {
		return nil, nil
	}
	values := make(map[string]any)
	source := make(map[string]PrefSource)
	for k, v := range DefaultPreferences {
		values[k] = v
		source[k] = SourceDefault
	}
	if home != "" {
		global, err := GlobalPreferences(home)
		if err != nil {
			return nil, fmt.Errorf("load global theia preferences: %w", err)
		}
		for k, v := range global {
			values[k] = v
			source[k] = SourceGlobal
		}
	}
	if home != "" && project.ConfigSourceHash() != "" {
		prefs, err := ProjectPreferences(home, project)
		if err != nil {
			return nil, fmt.Errorf("load project theia preferences: %w", err)
		}
		for k, v := range prefs {
			values[k] = v
			source[k] = SourceProject
		}
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]EffectivePref, 0, len(keys))
	for _, k := range keys {
		out = append(out, EffectivePref{Key: k, Value: values[k], Source: source[k]})
	}
	return out, nil
}

// ProjectPreferences returns the selected project override map (nil if unset).
func ProjectPreferences(home string, project model.Project) (map[string]any, error) {
	if home == "" || project.ConfigSourceHash() == "" {
		return nil, nil
	}
	return loadProjectFile(config.HostProjectConfigJSONPath(home, project.ConfigSourceHash()))
}

// GlobalPreferences returns the global override map (nil if unset).
func GlobalPreferences(home string) (map[string]any, error) {
	return readPrefsFile(GlobalPreferencesPath(home))
}

// GlobalPreferencesPath is the file global overrides are stored in.
func GlobalPreferencesPath(home string) string {
	return filepath.Join(config.HostToolConfigDir(home, "theia"), "preferences.json")
}

func loadProjectFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is resolved by application config logic.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Theia struct {
			Preferences map[string]any `json:"preferences"`
		} `json:"theia"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return wrapper.Theia.Preferences, nil
}

func readPrefsFile(path string) (map[string]any, error) {
	// #nosec G304 -- callers pass enclave-managed preference paths.
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var prefs map[string]any
	if err := json.Unmarshal(raw, &prefs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return prefs, nil
}
