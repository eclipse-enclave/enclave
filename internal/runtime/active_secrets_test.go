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
	"strings"
	"testing"

	"enclave/internal/auth"
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

func TestResolveEnvAliasValue(t *testing.T) {
	const globalFile = "/secrets/global.env"
	const projectFile = "/secrets/projects/hash/tool.env"

	layers := func(values ...map[string]string) []auth.SecretsLayer {
		result := make([]auth.SecretsLayer, 0, len(values))
		for i, value := range values {
			path := globalFile
			if i > 0 {
				path = projectFile
			}
			result = append(result, auth.SecretsLayer{Path: path, Values: value})
		}
		return result
	}

	tests := []struct {
		name       string
		hostEnv    map[string]string
		layers     []auth.SecretsLayer
		persisted  map[string]string
		wantValue  string
		wantSource string
		wantFound  bool
		wantErr    string
	}{{
		name:       "host env beats the secrets files",
		hostEnv:    map[string]string{"GH_TOKEN": "env-token"},
		layers:     layers(map[string]string{"GITHUB_TOKEN": "secrets-token"}),
		wantValue:  "env-token",
		wantSource: "env",
		wantFound:  true,
	}, {
		name:       "secrets files beat the persisted store",
		layers:     layers(map[string]string{"GITHUB_TOKEN": "rotated-token"}),
		persisted:  map[string]string{"GH_TOKEN": "stale-token"},
		wantValue:  "rotated-token",
		wantSource: "secrets",
		wantFound:  true,
	}, {
		name: "a later secrets layer beats an earlier one",
		layers: layers(
			map[string]string{"GH_TOKEN": "global-token"},
			map[string]string{"GITHUB_TOKEN": "project-token"},
		),
		wantValue:  "project-token",
		wantSource: "secrets",
		wantFound:  true,
	}, {
		name:    "aliases conflicting inside one secrets file fail",
		layers:  layers(map[string]string{"GH_TOKEN": "one", "GITHUB_TOKEN": "two"}),
		wantErr: globalFile,
	}, {
		name:    "aliases conflicting in the host environment fail",
		hostEnv: map[string]string{"GH_TOKEN": "one", "GITHUB_TOKEN": "two"},
		wantErr: "the host environment",
	}, {
		name:       "drift below the winning layer only warns",
		hostEnv:    map[string]string{"GH_TOKEN": "env-token"},
		layers:     layers(map[string]string{"GITHUB_TOKEN": "secrets-token"}),
		persisted:  map[string]string{"GHE_TOKEN": "stale-token"},
		wantValue:  "env-token",
		wantSource: "env",
		wantFound:  true,
	}, {
		name: "no alias set anywhere",
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := activeSecret{ID: "github-token", EnvVars: []string{"GH_TOKEN", "GITHUB_TOKEN", "GHE_TOKEN"}}
			for _, envVar := range secret.EnvVars {
				t.Setenv(envVar, tt.hostEnv[envVar])
			}

			value, source, found, err := resolveEnvAliasValue(secret, tt.layers, tt.persisted)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveEnvAliasValue() error = nil, want %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), "conflicting values across env aliases") || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveEnvAliasValue() error = %q, want conflict naming %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveEnvAliasValue() error = %v", err)
			}
			if found != tt.wantFound || value != tt.wantValue || source != tt.wantSource {
				t.Fatalf("resolveEnvAliasValue() = (%q, %q, %v), want (%q, %q, %v)", value, source, found, tt.wantValue, tt.wantSource, tt.wantFound)
			}
		})
	}
}
