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
	"enclave/internal/model"
)

func TestEmptyHomeSkipsProjectPreferences(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	path := config.HostProjectConfigJSONPath(home, "project")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPreferencesForProject("", model.Project{Hash: "project"}, true); err != nil {
		t.Fatalf("empty home read project preferences: %v", err)
	}
	if _, err := Effective("", model.Project{Hash: "project"}, true); err != nil {
		t.Fatalf("effective preferences read an omitted layer: %v", err)
	}
	if prefs, err := ProjectPreferences("", model.Project{Hash: "project"}); err != nil || prefs != nil {
		t.Fatalf("project preferences read an omitted layer: %v %v", prefs, err)
	}
}

func TestLoadPreferencesUsesResolvedConfigSource(t *testing.T) {
	home := t.TempDir()
	project := model.Project{Hash: "sibling", ConfigHash: "main"}
	for hash, value := range map[string]string{"main": "inherited", "sibling": "ignored"} {
		writeJSON(t, config.HostProjectConfigJSONPath(home, hash), map[string]any{"theia": map[string]any{"preferences": map[string]any{"key.x": value}}})
	}
	prefs, err := LoadPreferencesForProject(home, project, true)
	if err != nil {
		t.Fatal(err)
	}
	if prefs["key.x"] != "inherited" {
		t.Fatalf("wrong source: %v", prefs)
	}
	effective, err := Effective(home, project, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i, pref := range effective {
		if i > 0 && effective[i-1].Key >= pref.Key {
			t.Fatal("effective preferences are not sorted")
		}
		if pref.Key == "key.x" {
			found = true
			if pref.Value != "inherited" || pref.Source != SourceProject {
				t.Fatalf("wrong effective source: %+v", pref)
			}
		}
	}
	if !found {
		t.Fatal("effective preferences omitted the project value")
	}
}

func TestLoadPreferencesDefaultsOnly(t *testing.T) {
	got, err := LoadPreferencesForProject(t.TempDir(), model.Project{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got["ai-features.chat.defaultToolConfirmation"] != "always_allow" {
		t.Fatalf("missing default: %v", got)
	}
}

func TestLoadPreferencesGlobalOverride(t *testing.T) {
	home := t.TempDir()
	writeJSON(t, GlobalPreferencesPath(home), map[string]any{"ai-features.chat.defaultToolConfirmation": "ask", "editor.fontSize": 14})
	got, err := LoadPreferencesForProject(home, model.Project{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got["ai-features.chat.defaultToolConfirmation"] != "ask" || got["editor.fontSize"].(float64) != 14 {
		t.Fatalf("global override not applied: %v", got)
	}
}

func TestLoadPreferencesProjectBeatsGlobal(t *testing.T) {
	home := t.TempDir()
	project := model.Project{ConfigHash: "selected"}
	writeJSON(t, GlobalPreferencesPath(home), map[string]any{"key.x": "global"})
	writeJSON(t, config.HostProjectConfigJSONPath(home, project.ConfigHash), map[string]any{"theia": map[string]any{"preferences": map[string]any{"key.x": "project"}}})
	got, err := LoadPreferencesForProject(home, project, true)
	if err != nil {
		t.Fatal(err)
	}
	if got["key.x"] != "project" {
		t.Fatalf("project should override global: %v", got)
	}
}

func TestLoadPreferencesMissingFilesAreNotErrors(t *testing.T) {
	if _, err := LoadPreferencesForProject(t.TempDir(), model.Project{ConfigHash: "missing"}, true); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPreferencesYoloDisabledWithholdsAllLayers(t *testing.T) {
	home := t.TempDir()
	project := model.Project{ConfigHash: "selected"}
	writeJSON(t, GlobalPreferencesPath(home), map[string]any{"editor.fontSize": 16})
	writeJSON(t, config.HostProjectConfigJSONPath(home, project.ConfigHash), map[string]any{"theia": map[string]any{"preferences": map[string]any{"key.x": "project"}}})
	got, err := LoadPreferencesForProject(home, project, false)
	if err != nil || len(got) != 0 {
		t.Fatalf("non-yolo session received preferences: %v %v", got, err)
	}
	if prefs, err := Effective(home, project, false); err != nil || len(prefs) != 0 {
		t.Fatalf("non-yolo effective preferences: %v %v", prefs, err)
	}
}

func TestLoadPreferencesBadGlobalJSON(t *testing.T) {
	home := t.TempDir()
	path := GlobalPreferencesPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPreferencesForProject(home, model.Project{}, true); err == nil {
		t.Fatal("expected malformed JSON error")
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
