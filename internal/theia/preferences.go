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
	"strconv"
	"strings"

	"enclave/internal/config"
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

// MergeExternalAPI overlays Theia's separate-port external-API preferences for
// apiPort onto prefs (allocating prefs if nil) and returns it. An empty or
// non-numeric apiPort is a no-op. The externalApi.token preference is only set
// when apiToken is non-empty; otherwise it is left unset. These are functional
// preferences (they enable an API surface), not the yolo "always allow" trust
// preferences, so they are injected regardless of yolo mode when the caller
// passes a port — the matching host port must be published separately (see
// --theia-api-port). externalApi.hostname stays 0.0.0.0 so the service binds
// all in-container interfaces and is reachable through the published (loopback)
// host port.
func MergeExternalAPI(prefs map[string]any, apiPort, apiToken string) map[string]any {
	apiPort = strings.TrimSpace(apiPort)
	if apiPort == "" {
		return prefs
	}
	port, err := strconv.Atoi(apiPort)
	if err != nil {
		return prefs // defensive; CLI validation prevents this
	}
	if prefs == nil {
		prefs = make(map[string]any)
	}
	prefs["externalApi.delivery"] = "separatePort"
	prefs["externalApi.port"] = port
	prefs["externalApi.hostname"] = "0.0.0.0"
	if apiToken != "" {
		prefs["externalApi.token"] = apiToken
	}
	return prefs
}

// LoadPreferences returns the preferences to pass on launch. When yoloEnabled
// is false it returns nothing (these preferences only make sense for a yolo
// session). Otherwise it merges three sources, highest wins:
//
//  1. built-in DefaultPreferences
//  2. global:  ~/.config/enclave/tools/theia/preferences.json  (flat map)
//  3. project: ~/.config/enclave/projects/<hash>/config.json under {"theia":{"preferences":{...}}}
//
// Both roots honor $XDG_CONFIG_HOME on Linux (ignored on macOS).
//
// home or projectDir may be empty to skip that layer.
func LoadPreferences(home, projectDir string, yoloEnabled bool) (map[string]any, error) {
	// These preferences exist to put the in-IDE AI agent into "always allow"
	// mode, so when the session is not yolo we pass nothing at all: neither the
	// built-in defaults nor any global/project overrides.
	if !yoloEnabled {
		return nil, nil
	}
	merged := make(map[string]any, len(DefaultPreferences))
	for k, v := range DefaultPreferences {
		merged[k] = v
	}
	if home != "" {
		global, err := loadGlobal(home)
		if err != nil {
			return nil, fmt.Errorf("load global theia preferences: %w", err)
		}
		for k, v := range global {
			merged[k] = v
		}
	}
	if projectDir != "" {
		project, err := loadProject(projectDir)
		if err != nil {
			return nil, fmt.Errorf("load project theia preferences: %w", err)
		}
		for k, v := range project {
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

// Effective returns the merged preference set (same precedence as
// LoadPreferences) with each value tagged by its source layer, sorted by key.
// This is the transparency view for the UI: it shows exactly what will be sent
// to the IDE and why. Like LoadPreferences, a non-yolo session sends nothing,
// so the effective set is empty regardless of any global/project overrides.
func Effective(home, projectDir string, yoloEnabled bool) ([]EffectivePref, error) {
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
		global, err := loadGlobal(home)
		if err != nil {
			return nil, fmt.Errorf("load global theia preferences: %w", err)
		}
		for k, v := range global {
			values[k] = v
			source[k] = SourceGlobal
		}
	}
	if projectDir != "" {
		project, err := loadProject(projectDir)
		if err != nil {
			return nil, fmt.Errorf("load project theia preferences: %w", err)
		}
		for k, v := range project {
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

// GlobalPreferences returns the global override map (nil if unset).
func GlobalPreferences(home string) (map[string]any, error) { return loadGlobal(home) }

// ProjectPreferences returns the project override map (nil if unset).
func ProjectPreferences(projectDir string) (map[string]any, error) { return loadProject(projectDir) }

// GlobalPreferencesPath is the file global overrides are stored in.
func GlobalPreferencesPath(home string) string {
	return filepath.Join(config.HostToolConfigDir(home, "theia"), "preferences.json")
}

func loadGlobal(home string) (map[string]any, error) {
	return readPrefsFile(GlobalPreferencesPath(home))
}

func loadProject(projectDir string) (map[string]any, error) {
	path := config.ProjectConfigJSONPath(projectDir)
	if path == "" {
		return nil, nil
	}
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
