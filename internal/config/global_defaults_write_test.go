// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteGlobalDefaultCreatesAndMergesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	path, err := WriteGlobalDefault("backend", "podman")
	if err != nil {
		t.Fatalf("WriteGlobalDefault on missing file: %v", err)
	}
	want, err := GlobalConfigPath()
	if err != nil {
		t.Fatalf("GlobalConfigPath: %v", err)
	}
	if path != want {
		t.Fatalf("wrote %s, want %s", path, want)
	}
	if err := os.WriteFile(path, []byte(`{"tool": "codex", "backend": "docker", "tool_overrides": {"codex": {"slim": true}}}`), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if _, err := WriteGlobalDefault("backend", "podman"); err != nil {
		t.Fatalf("WriteGlobalDefault on existing file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("config is not valid JSON after write: %v\n%s", err, data)
	}
	if string(doc["backend"]) != `"podman"` {
		t.Fatalf("backend = %s, want \"podman\"", doc["backend"])
	}
	if string(doc["tool"]) != `"codex"` || len(doc["tool_overrides"]) == 0 {
		t.Fatalf("other keys were not preserved: %s", data)
	}
	global, _, warnings, err := LoadDefaults(t.TempDir())
	if err != nil || len(warnings) != 0 {
		t.Fatalf("LoadDefaults after write: err=%v warnings=%v", err, warnings)
	}
	if global.Backend != "podman" {
		t.Fatalf("LoadDefaults backend = %q, want podman", global.Backend)
	}
}

func TestWriteGlobalDefaultRejectsInvalidExistingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path, err := GlobalConfigPath()
	if err != nil {
		t.Fatalf("GlobalConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if _, err := WriteGlobalDefault("backend", "podman"); err == nil {
		t.Fatal("expected an error instead of overwriting an unparsable config")
	}
	if data, _ := os.ReadFile(path); string(data) != "{not json" {
		t.Fatalf("unparsable config was modified: %q", data)
	}
}
