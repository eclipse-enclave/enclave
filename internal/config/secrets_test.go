// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"strings"
	"testing"

	"enclave/internal/model"
)

// TestValidateAndNormalizeSecretConfigsCarriesFileAndParser proves the File
// source (path + parser) survives normalization. This is the seam that dropped
// the field before the fix (E2E T15): the runtime reads File, so losing it made
// file-sourced credentials inert.
func TestValidateAndNormalizeSecretConfigsCarriesFileAndParser(t *testing.T) {
	in := map[string]model.SecretConfig{
		"demo": {
			EnvVars: []string{"DEMO_KEY"},
			File:    &model.SecretFileSource{Path: "~/.creds.json", Parser: "json:auth.token"},
		},
	}
	out, err := validateAndNormalizeSecretConfigs(in)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	got := out["demo"]
	if got.File == nil {
		t.Fatalf("File dropped during normalization")
	}
	if got.File.Path != "~/.creds.json" || got.File.Parser != "json:auth.token" {
		t.Fatalf("File not carried through: %+v", got.File)
	}
}

// TestValidateAndNormalizeSecretConfigsPriorityRoundTrip covers priority
// survival and the empty->env-first normalization (only when a file source is
// present, so file-less secrets serialize unchanged).
func TestValidateAndNormalizeSecretConfigsPriorityRoundTrip(t *testing.T) {
	in := map[string]model.SecretConfig{
		"file-first-secret": {
			EnvVars:  []string{"FF_KEY"},
			File:     &model.SecretFileSource{Path: "~/.a.json", Parser: "json:a"},
			Priority: model.SecretPriorityFileFirst,
		},
		"empty-priority-with-file": {
			EnvVars: []string{"EP_KEY"},
			File:    &model.SecretFileSource{Path: "~/.b.json"},
			// Priority intentionally empty -> normalizes to env-first.
		},
		"empty-priority-no-file": {
			EnvVars: []string{"NF_KEY"},
			// No file, empty priority -> preserved empty (byte-identical golden).
		},
	}
	out, err := validateAndNormalizeSecretConfigs(in)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := out["file-first-secret"].Priority; got != model.SecretPriorityFileFirst {
		t.Fatalf("file-first priority = %q, want %q", got, model.SecretPriorityFileFirst)
	}
	if got := out["empty-priority-with-file"].Priority; got != model.SecretPriorityEnvFirst {
		t.Fatalf("empty priority with file = %q, want %q", got, model.SecretPriorityEnvFirst)
	}
	if got := out["empty-priority-no-file"].Priority; got != "" {
		t.Fatalf("empty priority without file = %q, want \"\" (preserved)", got)
	}
}

func TestValidateAndNormalizeSecretConfigsInvalidPriority(t *testing.T) {
	in := map[string]model.SecretConfig{
		"demo": {
			EnvVars:  []string{"DEMO_KEY"},
			Priority: "file-only",
		},
	}
	_, err := validateAndNormalizeSecretConfigs(in)
	if err == nil {
		t.Fatalf("expected invalid priority error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid priority") {
		t.Fatalf("error = %v, want it to mention invalid priority", err)
	}
}

func TestValidateAndNormalizeSecretConfigsInvalidParser(t *testing.T) {
	in := map[string]model.SecretConfig{
		"demo": {
			EnvVars: []string{"DEMO_KEY"},
			File:    &model.SecretFileSource{Path: "~/.creds.json", Parser: "/auth/token"},
		},
	}
	_, err := validateAndNormalizeSecretConfigs(in)
	if err == nil {
		t.Fatalf("expected invalid parser error, got nil")
	}
	if !strings.Contains(err.Error(), "parser") {
		t.Fatalf("error = %v, want it to mention the parser", err)
	}
}

func TestValidateAndNormalizeSecretConfigsEmptyFilePath(t *testing.T) {
	in := map[string]model.SecretConfig{
		"demo": {
			EnvVars: []string{"DEMO_KEY"},
			File:    &model.SecretFileSource{Path: "   ", Parser: "json:a"},
		},
	}
	_, err := validateAndNormalizeSecretConfigs(in)
	if err == nil {
		t.Fatalf("expected empty file path error, got nil")
	}
	if !strings.Contains(err.Error(), "non-empty path") {
		t.Fatalf("error = %v, want it to mention non-empty path", err)
	}
}

// TestValidateAndNormalizeSecretConfigsFilePointerNotAliased is the regression
// guard: the normalized File must be a clone, so mutating the caller's input
// after normalization does not leak into the result (and vice versa).
func TestValidateAndNormalizeSecretConfigsFilePointerNotAliased(t *testing.T) {
	src := &model.SecretFileSource{Path: "~/.creds.json", Parser: "json:auth.token"}
	in := map[string]model.SecretConfig{
		"demo": {EnvVars: []string{"DEMO_KEY"}, File: src},
	}
	out, err := validateAndNormalizeSecretConfigs(in)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	got := out["demo"].File
	if got == src {
		t.Fatalf("normalized File aliases the caller's pointer; want a clone")
	}
	// Mutating the input must not change the normalized copy.
	src.Path = "~/.mutated.json"
	if got.Path != "~/.creds.json" {
		t.Fatalf("normalized File.Path changed to %q after mutating input; not cloned", got.Path)
	}
}

// TestLoadToolExtensionCarriesFileSecret is the load-level seam test: a spec
// fixture declaring credentials.sources.<id>.file + priority: file-first must
// still carry File/Priority after going through the real LoadProfile /
// LoadToolExtension path (validateAndNormalizeSecretConfigs runs there).
func TestLoadToolExtensionCarriesFileSecret(t *testing.T) {
	paths := testPathsWithExtensions(t, "testdata/spec")

	ext, err := LoadToolExtension(paths, "filesecret")
	if err != nil {
		t.Fatalf("LoadToolExtension: %v", err)
	}
	assertFileSecretSurvived(t, ext.Secrets)

	p, err := LoadProfile(paths, "filesecret")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	assertFileSecretSurvived(t, p.Secrets)
}

func assertFileSecretSurvived(t *testing.T, secrets map[string]model.SecretConfig) {
	t.Helper()
	sc, ok := secrets["demo-file-secret"]
	if !ok {
		t.Fatalf("secret demo-file-secret missing from %v", secrets)
	}
	if sc.File == nil {
		t.Fatalf("File dropped on load; secret = %+v", sc)
	}
	if sc.File.Path != "~/.demo-creds.json" || sc.File.Parser != "json:auth.token" {
		t.Fatalf("File not carried through load: %+v", sc.File)
	}
	if sc.Priority != model.SecretPriorityFileFirst {
		t.Fatalf("Priority = %q, want %q", sc.Priority, model.SecretPriorityFileFirst)
	}
}
