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
	"slices"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

func bearerTokenRuntime(t *testing.T, tool string) *Runtime {
	t.Helper()
	paths := model.Paths{ToolsDir: filepath.Join("..", "..", "extensions", "tools")}
	profile, err := config.LoadProfile(paths, tool)
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range profile.DeclaredSecretEnvVars() {
		t.Setenv(env, "")
	}
	return runtimeWithProfile(t, profile)
}

func TestEnvBearerTokensUseGatewayPlaceholdersAcrossSessions(t *testing.T) {
	for _, tc := range []struct {
		tool, env, value, prefix, provider, header string
		hosts                                      []string
	}{
		{"claude", "CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-real-claude", "ENCLAVE_SECRET_", "anthropic", "authorization", []string{"api.anthropic.com"}},
		{"pi", "ANTHROPIC_OAUTH_TOKEN", "sk-ant-oat01-real-pi", "sk-ant-oat01-ENCLAVE_SECRET_", "anthropic", "authorization", []string{"api.anthropic.com"}},
		{"codex", "CODEX_ACCESS_TOKEN", "at-real-codex", "at-ENCLAVE_SECRET_", "openai-codex", "authorization", []string{"auth.openai.com", "chatgpt.com"}},
		{"codex", "CODEX_API_KEY", "real-openai", "ENCLAVE_SECRET_", "openai-codex", "authorization", []string{"*.openai.com", "api.openai.com"}},
		{"codex", "OPENAI_API_KEY", "real-openai", "ENCLAVE_SECRET_", "openai-codex", "authorization", []string{"*.openai.com", "api.openai.com"}},
	} {
		t.Run(tc.tool+"/"+tc.env, func(t *testing.T) {
			r := bearerTokenRuntime(t, tc.tool)
			t.Setenv(tc.env, tc.value)
			var previous string
			for range 2 {
				var env []string
				injection, err := newAuthManager(r).injectDeclaredSecrets(&captureHooks{}, authContextForRuntime(r), &env, nil, nil, nil, mustActiveSecrets(t, r))
				if err != nil {
					t.Fatal(err)
				}
				placeholder := envValue(env, tc.env)
				if !strings.HasPrefix(placeholder, tc.prefix) || placeholder == previous || placeholder == tc.value {
					t.Fatalf("invalid or reused placeholder %q", placeholder)
				}
				previous = placeholder
				if len(injection.SecretMapping.Entries) != 1 {
					t.Fatalf("mapping = %+v", injection.SecretMapping)
				}
				rule := injection.SecretMapping.Entries[0]
				if rule.Placeholder != placeholder || rule.Value != tc.value || rule.Header != tc.header || rule.Format != "Bearer %s" || !slices.Equal(rule.Hosts, tc.hosts) {
					t.Fatalf("release rule = %+v", rule)
				}
				if !injection.ProviderHasEnvCredential[tc.provider] {
					t.Fatal("provider did not recognize env credential")
				}
				if injection.SecretValues[tc.env] != tc.value {
					t.Fatal("host env store would persist a placeholder")
				}
				for _, entry := range env {
					if strings.Contains(entry, tc.value) {
						t.Fatalf("real credential in container env: %s", entry)
					}
				}
				if tc.tool == "codex" && tc.env != "CODEX_ACCESS_TOKEN" {
					if envValue(env, "CODEX_API_KEY") != placeholder || envValue(env, "OPENAI_API_KEY") != placeholder {
						t.Fatalf("Codex aliases differ: %v", env)
					}
				}
			}
		})
	}
}

func TestCodexAccessTokenRejectsUnsupportedTypesWithGateway(t *testing.T) {
	for _, source := range []string{"host", "secrets", "persisted"} {
		t.Run(source, func(t *testing.T) {
			r := bearerTokenRuntime(t, "codex")
			const jwt = "eyJhbGciOiJSUzI1NiJ9.jwt.signature"
			persisted := map[string]string{}
			var layers = secretsLayers(nil)
			switch source {
			case "host":
				t.Setenv("CODEX_ACCESS_TOKEN", jwt)
			case "secrets":
				layers = secretsLayers(map[string]string{"CODEX_ACCESS_TOKEN": jwt})
			case "persisted":
				persisted["CODEX_ACCESS_TOKEN"] = jwt
			}
			var env []string
			hooks := &captureHooks{}
			injection, err := newAuthManager(r).injectDeclaredSecrets(hooks, authContextForRuntime(r), &env, persisted, layers, nil, mustActiveSecrets(t, r))
			if err == nil || !strings.Contains(err.Error(), "CODEX_ACCESS_TOKEN") || !strings.Contains(err.Error(), "at-") || strings.Contains(err.Error(), jwt) {
				t.Fatalf("error = %v", err)
			}
			wantSource := map[string]string{"host": "env", "secrets": "secrets", "persisted": "persisted env store"}[source]
			if !strings.Contains(err.Error(), "from "+wantSource+":") {
				t.Fatalf("error does not identify credential source: %v", err)
			}
			if strings.Contains(err.Error(), "--reset-auth") != (source == "persisted") {
				t.Fatalf("reset advice should only apply to persisted credentials: %v", err)
			}
			if envValue(env, "CODEX_ACCESS_TOKEN") != "" || len(injection.SecretMapping.Entries) != 0 || len(hooks.afterInjected) != 0 {
				t.Fatal("unsupported token injected or released")
			}
		})
	}
}

func TestCodexAccessTokenKeepsJWTWhenGatewayDisabled(t *testing.T) {
	r := bearerTokenRuntime(t, "codex")
	t.Setenv("XDG_CONFIG_HOME", "")
	policy := config.HostNetworkPolicyPath(r.host.Home)
	if err := os.MkdirAll(filepath.Dir(policy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy, []byte(`{"mode":"unrestricted"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	const jwt = "eyJhbGciOiJSUzI1NiJ9.jwt.signature"
	t.Setenv("CODEX_ACCESS_TOKEN", jwt)
	var env []string
	injection, err := newAuthManager(r).injectDeclaredSecrets(&captureHooks{}, authContextForRuntime(r), &env, nil, nil, nil, mustActiveSecrets(t, r))
	if err != nil {
		t.Fatal(err)
	}
	if envValue(env, "CODEX_ACCESS_TOKEN") != jwt || len(injection.SecretMapping.Entries) != 0 {
		t.Fatal("unrestricted auth should remain unchanged")
	}
}

func TestEnvBearerTokensAreNotSuppressedByNoAPIKey(t *testing.T) {
	for _, tc := range []struct{ tool, env, value string }{
		{"claude", "CLAUDE_CODE_OAUTH_TOKEN", "real-token"},
		{"pi", "ANTHROPIC_OAUTH_TOKEN", "sk-ant-oat01-real-token"},
		{"codex", "CODEX_ACCESS_TOKEN", "at-real-token"},
	} {
		t.Run(tc.tool+"/"+tc.env, func(t *testing.T) {
			r := bearerTokenRuntime(t, tc.tool)
			r.auth.NoAPIKey = true
			t.Setenv(tc.env, tc.value)
			var env []string
			injection, err := newAuthManager(r).injectDeclaredSecrets(&captureHooks{}, authContextForRuntime(r), &env, nil, nil, nil, mustActiveSecrets(t, r))
			if err != nil {
				t.Fatal(err)
			}
			if envValue(env, tc.env) == "" || len(injection.SecretMapping.Entries) != 1 {
				t.Fatal("token suppressed")
			}
		})
	}
}

func TestPrepareRecordsOnlyPlaceholdersForAuthSync(t *testing.T) {
	r := bearerTokenRuntime(t, "codex")
	t.Setenv("CODEX_API_KEY", "real-key")
	t.Setenv("CODEX_ACCESS_TOKEN", "at-real-token")
	var env []string
	_, mapping, err := newAuthManager(r).Prepare(&env, storeSet{})
	if err != nil {
		t.Fatal(err)
	}
	recorded := strings.Fields(envValue(env, model.EnvSecretPlaceholders))
	if len(recorded) != 2 {
		t.Fatalf("recorded placeholders = %v", recorded)
	}
	for _, rule := range mapping.Entries {
		if !slices.Contains(recorded, rule.Placeholder) || slices.Contains(recorded, rule.Value) {
			t.Fatal("incorrect placeholder metadata")
		}
	}
}
