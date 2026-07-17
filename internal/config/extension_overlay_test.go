// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"enclave/internal/model"
)

func TestResolveToolFilePrefersUserOverride(t *testing.T) {
	tmp := t.TempDir()
	builtinTools := filepath.Join(tmp, "extensions", "tools")
	userTools := filepath.Join(tmp, ".enclave", "extensions", "tools")
	writeTestFile(t, filepath.Join(builtinTools, "claude", SpecFilename), `{"name":"claude"}`)
	writeTestFile(t, filepath.Join(userTools, "claude", SpecFilename), `{"name":"claude-user"}`)

	paths := model.Paths{ToolsDir: builtinTools, UserToolsDir: userTools}
	path, ok := ResolveToolFile(paths, "claude", SpecFilename)
	if !ok {
		t.Fatal("ResolveToolFile returned not found")
	}
	if path != filepath.Join(userTools, "claude", SpecFilename) {
		t.Fatalf("expected user override path, got %s", path)
	}
}

func TestResolveToolFileFallsBackToBuiltin(t *testing.T) {
	tmp := t.TempDir()
	builtinTools := filepath.Join(tmp, "extensions", "tools")
	userTools := filepath.Join(tmp, ".enclave", "extensions", "tools")
	writeTestFile(t, filepath.Join(builtinTools, "claude", SpecFilename), `{"name":"claude"}`)

	paths := model.Paths{ToolsDir: builtinTools, UserToolsDir: userTools}
	path, ok := ResolveToolFile(paths, "claude", SpecFilename)
	if !ok {
		t.Fatal("ResolveToolFile returned not found")
	}
	if path != filepath.Join(builtinTools, "claude", SpecFilename) {
		t.Fatalf("expected builtin path, got %s", path)
	}
}

func TestResolveToolFileMissing(t *testing.T) {
	tmp := t.TempDir()
	paths := model.Paths{
		ToolsDir:     filepath.Join(tmp, "extensions", "tools"),
		UserToolsDir: filepath.Join(tmp, ".enclave", "extensions", "tools"),
	}
	if _, ok := ResolveToolFile(paths, "missing", SpecFilename); ok {
		t.Fatal("expected missing file")
	}
}

func TestResolveFeatureFilePrefersUserOverride(t *testing.T) {
	tmp := t.TempDir()
	builtinFeatures := filepath.Join(tmp, "extensions", "features")
	userFeatures := filepath.Join(tmp, ".enclave", "extensions", "features")
	writeTestFile(t, filepath.Join(builtinFeatures, "python-dev", SpecFilename), `{"name":"python-dev","type":"feature"}`)
	writeTestFile(t, filepath.Join(userFeatures, "python-dev", SpecFilename), `{"name":"python-dev","type":"feature","description":"override"}`)

	paths := model.Paths{FeaturesDir: builtinFeatures, UserFeaturesDir: userFeatures}
	path, ok := ResolveFeatureFile(paths, "python-dev", SpecFilename)
	if !ok {
		t.Fatal("ResolveFeatureFile returned not found")
	}
	if path != filepath.Join(userFeatures, "python-dev", SpecFilename) {
		t.Fatalf("expected user override path, got %s", path)
	}
}

func TestResolveToolAndFeatureDirs(t *testing.T) {
	tmp := t.TempDir()
	builtinTools := filepath.Join(tmp, "extensions", "tools")
	userTools := filepath.Join(tmp, ".enclave", "extensions", "tools")
	builtinFeatures := filepath.Join(tmp, "extensions", "features")
	userFeatures := filepath.Join(tmp, ".enclave", "extensions", "features")
	if err := os.MkdirAll(filepath.Join(builtinTools, "claude"), 0o755); err != nil {
		t.Fatalf("mkdir builtin tool: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(userTools, "claude"), 0o755); err != nil {
		t.Fatalf("mkdir user tool: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(userFeatures, "python-dev"), 0o755); err != nil {
		t.Fatalf("mkdir user feature: %v", err)
	}

	paths := model.Paths{
		ToolsDir:          builtinTools,
		UserToolsDir:      userTools,
		FeaturesDir:       builtinFeatures,
		UserFeaturesDir:   userFeatures,
		ExtensionsDir:     filepath.Join(tmp, "extensions"),
		UserExtensionsDir: filepath.Join(tmp, ".enclave", "extensions"),
	}
	builtinToolDir, userToolDir := ResolveToolDirs(paths, "claude")
	if builtinToolDir == "" || userToolDir == "" {
		t.Fatalf("expected both tool dirs, got builtin=%q user=%q", builtinToolDir, userToolDir)
	}
	builtinFeatureDir, userFeatureDir := ResolveFeatureDirs(paths, "python-dev")
	if builtinFeatureDir != "" || userFeatureDir == "" {
		t.Fatalf("expected user-only feature dir, got builtin=%q user=%q", builtinFeatureDir, userFeatureDir)
	}
}

func TestListToolsMergesAndDeduplicates(t *testing.T) {
	tmp := t.TempDir()
	builtinTools := filepath.Join(tmp, "extensions", "tools")
	userTools := filepath.Join(tmp, ".enclave", "extensions", "tools")

	writeTestFile(t, filepath.Join(builtinTools, "claude", SpecFilename), `{"schemaVersion":"1","kind":"sandbox","name":"claude"}`)
	writeTestFile(t, filepath.Join(builtinTools, "codex", SpecFilename), `{"schemaVersion":"1","kind":"sandbox","name":"codex"}`)
	writeTestFile(t, filepath.Join(userTools, "claude", "templates", "settings.json"), `{}`)
	writeTestFile(t, filepath.Join(userTools, "my-tool", SpecFilename), `{"schemaVersion":"1","kind":"sandbox","name":"my-tool"}`)

	paths := model.Paths{ToolsDir: builtinTools, UserToolsDir: userTools}
	got, err := ListTools(paths)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := []string{"claude", "codex", "my-tool"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListTools=%v want=%v", got, want)
	}
}

func TestListFeaturesMergesAndDeduplicates(t *testing.T) {
	tmp := t.TempDir()
	builtinFeatures := filepath.Join(tmp, "extensions", "features")
	userFeatures := filepath.Join(tmp, ".enclave", "extensions", "features")

	writeTestFile(t, filepath.Join(builtinFeatures, "github-cli", SpecFilename), `{"schemaVersion":"1","kind":"mixin","name":"github-cli","priority":40}`)
	writeTestFile(t, filepath.Join(builtinFeatures, "shared", SpecFilename), `{"schemaVersion":"1","kind":"mixin","name":"shared","priority":30}`)
	writeTestFile(t, filepath.Join(userFeatures, "shared", SpecFilename), `{"schemaVersion":"1","kind":"mixin","name":"shared","priority":5}`)
	writeTestFile(t, filepath.Join(userFeatures, "python-dev", SpecFilename), `{"schemaVersion":"1","kind":"mixin","name":"python-dev","priority":20}`)

	paths := model.Paths{FeaturesDir: builtinFeatures, UserFeaturesDir: userFeatures}
	features, err := ListFeatures(paths)
	if err != nil {
		t.Fatalf("ListFeatures: %v", err)
	}
	var names []string
	for _, feature := range features {
		names = append(names, feature.Name)
	}
	want := []string{"shared", "python-dev", "github-cli"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("ListFeatures names=%v want=%v", names, want)
	}
}

func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
