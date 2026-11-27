// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package apikeys

import (
	"slices"
	"strings"
	"testing"

	"enclave/internal/model"
)

func testProfiles() []model.Profile {
	notAPIKey := false
	return []model.Profile{
		{
			Name: "claude",
			Providers: []model.ProviderConfig{
				{Name: "anthropic", CredentialSecrets: []string{"anthropic-api-key", "claude-code-oauth-token"}},
			},
			Secrets: map[string]model.SecretConfig{
				"anthropic-api-key":       {EnvVars: []string{"ANTHROPIC_API_KEY"}},
				"claude-code-oauth-token": {EnvVars: []string{"CLAUDE_CODE_OAUTH_TOKEN"}, APIKey: &notAPIKey},
			},
		},
		{
			Name: "codex",
			Providers: []model.ProviderConfig{
				{Name: "openai-codex", CredentialSecrets: []string{"openai-api-key"}},
			},
			Secrets: map[string]model.SecretConfig{
				"openai-api-key": {EnvVars: []string{"CODEX_API_KEY", "OPENAI_API_KEY"}},
			},
		},
		{
			Name: "pi",
			Providers: []model.ProviderConfig{
				{Name: "openai", CredentialSecrets: []string{"openai-api-key"}},
				{Name: "anthropic", CredentialSecrets: []string{"anthropic-api-key"}},
				{Name: "google", CredentialSecrets: []string{"gemini-api-key"}},
			},
			Secrets: map[string]model.SecretConfig{
				"openai-api-key":    {EnvVars: []string{"OPENAI_API_KEY"}},
				"anthropic-api-key": {EnvVars: []string{"ANTHROPIC_API_KEY"}},
				"gemini-api-key": {
					EnvVars: []string{"GEMINI_API_KEY"},
					Release: &model.SecretReleaseConfig{HTTP: &model.HTTPSecretReleaseConfig{RequiredValuePrefix: "AIza"}},
				},
			},
		},
		{
			Name:      "copilot",
			Providers: []model.ProviderConfig{{Name: "github", CredentialSecrets: []string{"copilot-github-token"}}},
			Secrets:   map[string]model.SecretConfig{"copilot-github-token": {EnvVars: []string{"COPILOT_GITHUB_TOKEN"}}},
		},
	}
}

func TestCatalog(t *testing.T) {
	keys := Catalog(testProfiles())
	if got := Names(keys); !slices.Equal(got, []string{"anthropic", "copilot-github", "gemini", "openai"}) {
		t.Fatalf("names = %v", got)
	}
	anthropic, _ := Find(keys, "anthropic")
	if !slices.Equal(anthropic.Tools(), []string{"claude", "pi"}) || anthropic.Variable != "ANTHROPIC_API_KEY" {
		t.Fatalf("anthropic = %+v", anthropic)
	}
	openai, _ := Find(keys, "openai")
	if openai.Variable != "OPENAI_API_KEY" {
		t.Fatalf("openai variable = %q, want the alias most tools declare", openai.Variable)
	}
	if !slices.Equal(openai.EnvVars, []string{"CODEX_API_KEY", "OPENAI_API_KEY"}) {
		t.Fatalf("openai aliases = %v", openai.EnvVars)
	}
	gemini, _ := Find(keys, "gemini")
	i := slices.IndexFunc(gemini.Uses, func(use Use) bool { return use.Tool == "pi" })
	if i < 0 || gemini.Uses[i].RequiredPrefix != "AIza" {
		t.Fatalf("gemini uses = %+v", gemini.Uses)
	}
}

func TestFind(t *testing.T) {
	keys := Catalog(testProfiles())
	tests := map[string]string{
		"anthropic":            "anthropic",
		"Anthropic":            "anthropic",
		"google":               "gemini",
		"gemini-api-key":       "gemini",
		"CODEX_API_KEY":        "openai",
		"openai-codex":         "openai",
		"github":               "copilot-github",
		"copilot":              "copilot-github",
		"COPILOT_GITHUB_TOKEN": "copilot-github",
	}
	for name, want := range tests {
		key, err := Find(keys, name)
		if err != nil || key.Name != want {
			t.Fatalf("Find(%q) = %q, %v; want %q", name, key.Name, err, want)
		}
	}
	for _, name := range []string{"claude", "CLAUDE_CODE_OAUTH_TOKEN", "mistral"} {
		if _, err := Find(keys, name); err == nil || !strings.Contains(err.Error(), "unknown API key") {
			t.Fatalf("Find(%q) err = %v", name, err)
		}
	}
}

func TestCatalogKeepsVendorServicesApart(t *testing.T) {
	profiles := []model.Profile{{
		Name: "azure-tool",
		Providers: []model.ProviderConfig{
			{Name: "openai", CredentialSecrets: []string{"azure-openai-api-key"}},
			{Name: "speech", CredentialSecrets: []string{"azure-speech-api-key"}},
		},
		Secrets: map[string]model.SecretConfig{
			"azure-openai-api-key": {EnvVars: []string{"AZURE_OPENAI_API_KEY"}},
			"azure-speech-api-key": {EnvVars: []string{"AZURE_SPEECH_KEY"}},
		},
	}}
	keys := Catalog(profiles)
	if got := Names(keys); !slices.Equal(got, []string{"azure-openai", "azure-speech"}) {
		t.Fatalf("names = %v", got)
	}
	if _, err := Find(keys, "azure"); err == nil || !strings.Contains(err.Error(), "matches several") {
		t.Fatalf("Find(azure) err = %v", err)
	}
	if key, err := Find(keys, "AZURE_SPEECH_KEY"); err != nil || key.Name != "azure-speech" {
		t.Fatalf("Find(AZURE_SPEECH_KEY) = %q, %v", key.Name, err)
	}
}
