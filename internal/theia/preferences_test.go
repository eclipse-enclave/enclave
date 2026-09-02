// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package theia

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/config"
)

func TestLoadPreferences_DefaultsOnly(t *testing.T) {
	got, err := LoadPreferencesForProject(t.TempDir(), "", true)
	if err != nil {
		t.Fatalf("LoadPreferencesForProject: %v", err)
	}
	if got["ai-features.chat.defaultToolConfirmation"] != "always_allow" {
		t.Fatalf("missing default: %v", got)
	}
}

func TestLoadPreferences_GlobalOverride(t *testing.T) {
	home := t.TempDir()
	writeJSON(t, filepath.Join(config.HostToolConfigDir(home, "theia"), "preferences.json"),
		map[string]any{
			"ai-features.chat.defaultToolConfirmation": "ask",
			"editor.fontSize":                          14,
		})

	got, err := LoadPreferencesForProject(home, "", true)
	if err != nil {
		t.Fatalf("LoadPreferencesForProject: %v", err)
	}
	if got["ai-features.chat.defaultToolConfirmation"] != "ask" {
		t.Fatalf("global override not applied: %v", got)
	}
	if got["editor.fontSize"].(float64) != 14 {
		t.Fatalf("global key missing: %v", got)
	}
}

func TestLoadPreferences_ProjectBeatsGlobal(t *testing.T) {
	home := t.TempDir()
	projectHash := "abc123abc123"
	writeJSON(t, filepath.Join(config.HostToolConfigDir(home, "theia"), "preferences.json"),
		map[string]any{"key.x": "global"})
	projectConfigPath := config.HostProjectConfigJSONPath(home, projectHash)
	writeJSON(t, projectConfigPath,
		map[string]any{"theia": map[string]any{"preferences": map[string]any{"key.x": "project"}}})

	got, err := LoadPreferencesForProject(home, projectHash, true)
	if err != nil {
		t.Fatalf("LoadPreferencesForProject: %v", err)
	}
	if got["key.x"] != "project" {
		t.Fatalf("project should override global: %v", got)
	}
}

func TestLoadPreferences_MissingFilesAreNotErrors(t *testing.T) {
	if _, err := LoadPreferencesForProject(t.TempDir(), "missingproject", true); err != nil {
		t.Fatalf("missing files should be silent, got %v", err)
	}
}

func TestLoadPreferences_YoloDisabledSkipsDefaults(t *testing.T) {
	got, err := LoadPreferencesForProject(t.TempDir(), "", false)
	if err != nil {
		t.Fatalf("LoadPreferencesForProject: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map when yolo disabled and no overrides, got %v", got)
	}
}

func TestLoadPreferences_YoloDisabledWithholdsOverridesToo(t *testing.T) {
	home := t.TempDir()
	writeJSON(t, filepath.Join(config.HostToolConfigDir(home, "theia"), "preferences.json"),
		map[string]any{"editor.fontSize": 16})
	got, err := LoadPreferencesForProject(home, "", false)
	if err != nil {
		t.Fatalf("LoadPreferencesForProject: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("yolo disabled must pass nothing, even with an override file: %v", got)
	}
}

func TestLoadPreferences_BadGlobalJSON(t *testing.T) {
	home := t.TempDir()
	dir := config.HostToolConfigDir(home, "theia")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "preferences.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPreferencesForProject(home, "", true); err == nil {
		t.Fatal("expected parse error for malformed JSON")
	}
}

func writeJSON(t *testing.T, path string, payload any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
