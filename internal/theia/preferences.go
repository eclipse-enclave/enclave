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

// LoadPreferencesForProject returns the preferences to pass on launch for an
// already-resolved project namespace. When yoloEnabled is false it returns
// nothing. Otherwise it merges built-in, global, and project preferences in
// that order, with the project layer taking precedence.
func LoadPreferencesForProject(home, projectHash string, yoloEnabled bool) (map[string]any, error) {
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
	if home != "" && projectHash != "" {
		project, err := loadProjectByHash(home, projectHash)
		if err != nil {
			return nil, fmt.Errorf("load project theia preferences: %w", err)
		}
		for k, v := range project {
			merged[k] = v
		}
	}
	return merged, nil
}

// GlobalPreferencesPath is the file global overrides are stored in.
func GlobalPreferencesPath(home string) string {
	return filepath.Join(config.HostToolConfigDir(home, "theia"), "preferences.json")
}

func loadGlobal(home string) (map[string]any, error) {
	return readPrefsFile(GlobalPreferencesPath(home))
}

func loadProjectByHash(home string, projectHash string) (map[string]any, error) {
	path := config.HostProjectConfigJSONPath(home, projectHash)
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
