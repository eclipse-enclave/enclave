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

func TestMergeExternalAPI_InjectsKeys(t *testing.T) {
	got := MergeExternalAPI(nil, "3333", "")
	if got == nil {
		t.Fatal("expected non-nil map for a valid port")
	}
	if got["externalApi.delivery"] != "separatePort" {
		t.Errorf("delivery = %v", got["externalApi.delivery"])
	}
	if got["externalApi.port"] != 3333 {
		t.Errorf("port = %v (want int 3333)", got["externalApi.port"])
	}
	if got["externalApi.hostname"] != "0.0.0.0" {
		t.Errorf("hostname = %v", got["externalApi.hostname"])
	}
	if _, ok := got["externalApi.token"]; ok {
		t.Errorf("expected no token preference when none provided, got %v", got["externalApi.token"])
	}
}

func TestMergeExternalAPI_CustomToken(t *testing.T) {
	got := MergeExternalAPI(nil, "3333", "s3cr3t")
	if got["externalApi.token"] != "s3cr3t" {
		t.Errorf("token = %v (want s3cr3t)", got["externalApi.token"])
	}
}

func TestMergeExternalAPI_EmptyIsNoop(t *testing.T) {
	if got := MergeExternalAPI(nil, "", "s3cr3t"); got != nil {
		t.Errorf("expected nil passthrough for empty port, got %v", got)
	}
	if got := MergeExternalAPI(nil, "  ", ""); got != nil {
		t.Errorf("expected nil passthrough for blank port, got %v", got)
	}
}

func TestMergeExternalAPI_PreservesExistingKeys(t *testing.T) {
	prefs := map[string]any{"ai-features.chat.defaultToolConfirmation": "always_allow"}
	got := MergeExternalAPI(prefs, "8080", "")
	if got["ai-features.chat.defaultToolConfirmation"] != "always_allow" {
		t.Errorf("dropped existing key: %v", got)
	}
	if got["externalApi.port"] != 8080 {
		t.Errorf("port = %v", got["externalApi.port"])
	}
}

func TestLoadPreferences_DefaultsOnly(t *testing.T) {
	got, err := LoadPreferences(t.TempDir(), t.TempDir(), true)
	if err != nil {
		t.Fatalf("LoadPreferences: %v", err)
	}
	if got["ai-features.chat.defaultToolConfirmation"] != "always_allow" {
		t.Fatalf("missing default: %v", got)
	}
}

func TestLoadPreferences_GlobalOverride(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	writeJSON(t, filepath.Join(config.HostToolConfigDir(home, "theia"), "preferences.json"),
		map[string]any{
			"ai-features.chat.defaultToolConfirmation": "ask",
			"editor.fontSize":                          14,
		})

	got, err := LoadPreferences(home, project, true)
	if err != nil {
		t.Fatalf("LoadPreferences: %v", err)
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
	// loadProject resolves the hash-keyed config-root path via the real host
	// home, so anchor HOME inside the test tempdir. The package TestMain unsets
	// the XDG roots so they cannot override this home.
	t.Setenv("HOME", home)
	project := t.TempDir()
	writeJSON(t, filepath.Join(config.HostToolConfigDir(home, "theia"), "preferences.json"),
		map[string]any{"key.x": "global"})
	projectConfigPath := config.ProjectConfigJSONPath(project)
	if projectConfigPath == "" {
		t.Fatal("ProjectConfigJSONPath returned empty path")
	}
	writeJSON(t, projectConfigPath,
		map[string]any{"theia": map[string]any{"preferences": map[string]any{"key.x": "project"}}})

	got, err := LoadPreferences(home, project, true)
	if err != nil {
		t.Fatalf("LoadPreferences: %v", err)
	}
	if got["key.x"] != "project" {
		t.Fatalf("project should override global: %v", got)
	}
}

func TestLoadPreferences_MissingFilesAreNotErrors(t *testing.T) {
	if _, err := LoadPreferences(t.TempDir(), t.TempDir(), true); err != nil {
		t.Fatalf("missing files should be silent, got %v", err)
	}
}

func TestLoadPreferences_YoloDisabledSkipsDefaults(t *testing.T) {
	got, err := LoadPreferences(t.TempDir(), t.TempDir(), false)
	if err != nil {
		t.Fatalf("LoadPreferences: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map when yolo disabled and no overrides, got %v", got)
	}
}

func TestLoadPreferences_YoloDisabledWithholdsOverridesToo(t *testing.T) {
	home := t.TempDir()
	writeJSON(t, filepath.Join(config.HostToolConfigDir(home, "theia"), "preferences.json"),
		map[string]any{"editor.fontSize": 16})
	got, err := LoadPreferences(home, "", false)
	if err != nil {
		t.Fatalf("LoadPreferences: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("yolo disabled must pass nothing, even with an override file: %v", got)
	}
}

func TestEffective_YoloDisabledWithholdsOverridesToo(t *testing.T) {
	home := t.TempDir()
	writeJSON(t, filepath.Join(config.HostToolConfigDir(home, "theia"), "preferences.json"),
		map[string]any{"editor.fontSize": 16})
	got, err := Effective(home, "", false)
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("yolo disabled must show nothing, even with an override file: %v", got)
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
	if _, err := LoadPreferences(home, "", true); err == nil {
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
