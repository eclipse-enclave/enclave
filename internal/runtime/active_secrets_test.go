// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/model"
)

func writeSecretFile(t *testing.T, home, rel, content string) {
	t.Helper()
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveActiveSecretValueFileOnly(t *testing.T) {
	home := t.TempDir()
	writeSecretFile(t, home, ".config/svc/token.json", `{"auth":{"token":"file-token"}}`)

	secret := activeSecret{
		ID:      "svc",
		EnvVars: []string{"SVC_TOKEN"},
		File:    &model.SecretFileSource{Path: "~/.config/svc/token.json", Parser: "json:auth.token"},
	}
	// No env alias present anywhere -> the file value is injected.
	value, source, found, err := resolveActiveSecretValue(secret, home, nil, nil)
	if err != nil {
		t.Fatalf("resolveActiveSecretValue() error = %v", err)
	}
	if !found || value != "file-token" {
		t.Fatalf("resolveActiveSecretValue() = (%q, %v), want (%q, true)", value, found, "file-token")
	}
	if source != "file" {
		t.Fatalf("source = %q, want %q", source, "file")
	}
}

func TestResolveActiveSecretValueFileFirstBeatsEnv(t *testing.T) {
	home := t.TempDir()
	writeSecretFile(t, home, "token.txt", "file-value\n")

	t.Setenv("SVC_TOKEN", "env-value")
	secret := activeSecret{
		ID:       "svc",
		EnvVars:  []string{"SVC_TOKEN"},
		File:     &model.SecretFileSource{Path: "~/token.txt"},
		Priority: model.SecretPriorityFileFirst,
	}
	value, source, found, err := resolveActiveSecretValue(secret, home, nil, nil)
	if err != nil {
		t.Fatalf("resolveActiveSecretValue() error = %v", err)
	}
	if !found || value != "file-value" {
		t.Fatalf("file-first: got (%q, %v), want (%q, true)", value, found, "file-value")
	}
	if source != "file" {
		t.Fatalf("file-first source = %q, want %q", source, "file")
	}
}

func TestResolveActiveSecretValueEnvFirstBeatsFile(t *testing.T) {
	home := t.TempDir()
	writeSecretFile(t, home, "token.txt", "file-value\n")

	t.Setenv("SVC_TOKEN", "env-value")
	secret := activeSecret{
		ID:      "svc",
		EnvVars: []string{"SVC_TOKEN"},
		File:    &model.SecretFileSource{Path: "~/token.txt"},
		// Priority empty -> env-first default.
	}
	value, source, found, err := resolveActiveSecretValue(secret, home, nil, nil)
	if err != nil {
		t.Fatalf("resolveActiveSecretValue() error = %v", err)
	}
	if !found || value != "env-value" {
		t.Fatalf("env-first: got (%q, %v), want (%q, true)", value, found, "env-value")
	}
	if source != "env" {
		t.Fatalf("env-first source = %q, want %q", source, "env")
	}
}
