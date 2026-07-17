// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "testing"

func TestProviderSecurestorageDirEnv(t *testing.T) {
	profile := Profile{
		Providers: []ProviderConfig{
			{Name: "noauthenv"},
			{Name: "anthropic", SecurestorageDirEnv: "CLAUDE_SECURESTORAGE_CONFIG_DIR"},
		},
	}
	if got := profile.ProviderSecurestorageDirEnv(); got != "CLAUDE_SECURESTORAGE_CONFIG_DIR" {
		t.Fatalf("ProviderSecurestorageDirEnv = %q, want CLAUDE_SECURESTORAGE_CONFIG_DIR", got)
	}

	none := Profile{Providers: []ProviderConfig{{Name: "openai"}}}
	if got := none.ProviderSecurestorageDirEnv(); got != "" {
		t.Fatalf("ProviderSecurestorageDirEnv = %q, want empty", got)
	}
}

func TestProviderAPIKeySecretIDsUsesSecretAPIKeyMetadata(t *testing.T) {
	oauthIsAPIKey := false
	profile := Profile{
		Secrets: map[string]SecretConfig{
			"claude-code-oauth-token": {APIKey: &oauthIsAPIKey},
		},
		Providers: []ProviderConfig{
			{
				CredentialSecrets: []string{"anthropic-api-key", "claude-code-oauth-token"},
			},
		},
	}

	secretIDs := profile.ProviderAPIKeySecretIDs()

	if !secretIDs["anthropic-api-key"] {
		t.Fatalf("ProviderAPIKeySecretIDs()[anthropic-api-key] = false, want true")
	}
	if secretIDs["claude-code-oauth-token"] {
		t.Fatalf("ProviderAPIKeySecretIDs()[claude-code-oauth-token] = true, want false")
	}
}

func TestProviderAPIKeySecretIDsDefaultsToCredentialSecrets(t *testing.T) {
	profile := Profile{
		Providers: []ProviderConfig{
			{CredentialSecrets: []string{"legacy-api-key", "legacy-token"}},
		},
	}

	secretIDs := profile.ProviderAPIKeySecretIDs()

	for _, secretID := range []string{"legacy-api-key", "legacy-token"} {
		if !secretIDs[secretID] {
			t.Fatalf("ProviderAPIKeySecretIDs()[%q] = false, want true", secretID)
		}
	}
}
