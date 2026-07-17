// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/auth"
	"enclave/internal/config"
	"enclave/internal/model"
)

func boolPtr(value bool) *bool {
	return &value
}

type stubHooks struct{}

func (stubHooks) OnAuthReady(auth.Context) (bool, error)                 { return false, nil }
func (stubHooks) AfterEnvInjected(auth.Context, map[string]string) error { return nil }
func (stubHooks) FinalizeAuth(auth.Context, model.AuthState) error       { return nil }

type captureHooks struct {
	afterInjected map[string]string
}

func (c *captureHooks) OnAuthReady(auth.Context) (bool, error) { return false, nil }

func (c *captureHooks) AfterEnvInjected(_ auth.Context, injected map[string]string) error {
	c.afterInjected = map[string]string{}
	for key, value := range injected {
		c.afterInjected[key] = value
	}
	return nil
}

func (c *captureHooks) FinalizeAuth(auth.Context, model.AuthState) error { return nil }

type failingPlaceholderResolver struct{}

func (failingPlaceholderResolver) ResolvePlaceholder(string) (string, error) {
	return "", fmt.Errorf("boom")
}

func TestInjectPassEnvRegistersInjectedKeys(t *testing.T) {
	r := &Runtime{
		auth: model.AuthOptions{PassEnv: []string{"MY_TOKEN"}},
	}
	m := newAuthManager(r)

	t.Setenv("MY_TOKEN", "from-host")

	var env []string
	persistedEnv := map[string]string{}
	injectedKeys := map[string]string{}

	m.injectPassEnv(&env, persistedEnv, injectedKeys)

	if got := injectedKeys["MY_TOKEN"]; got != "from-host" {
		t.Fatalf("injectedKeys[MY_TOKEN] = %q, want %q", got, "from-host")
	}
	if got := envValue(env, "MY_TOKEN"); got != "from-host" {
		t.Fatalf("MY_TOKEN = %q, want %q", got, "from-host")
	}
}

func TestInjectPassEnvSkipsAlreadyInjectedKeys(t *testing.T) {
	r := &Runtime{
		auth: model.AuthOptions{PassEnv: []string{"GH_TOKEN"}},
	}
	m := newAuthManager(r)

	t.Setenv("GH_TOKEN", "from-host")

	var env []string
	persistedEnv := map[string]string{}
	injectedKeys := map[string]string{"GH_TOKEN": "placeholder"}

	passValues := m.injectPassEnv(&env, persistedEnv, injectedKeys)

	if len(passValues) != 0 {
		t.Fatalf("passValues = %v, want none", passValues)
	}
	if len(env) != 0 {
		t.Fatalf("env = %v, want empty", env)
	}
}

func TestMergeEnvForPersistPassEnvWins(t *testing.T) {
	existing := map[string]string{"OLD": "old-value"}
	declaredSecrets := map[string]string{
		"API_KEY":     "api-value",
		"SHARED":      "from-declared-secret",
		"SECRET_ONLY": "secret",
	}
	passEnv := map[string]string{"SHARED": "from-pass-env", "PASS_ONLY": "pass"}

	result := mergeEnvForPersist(existing, declaredSecrets, passEnv)

	checks := map[string]string{
		"OLD":         "old-value",
		"API_KEY":     "api-value",
		"SHARED":      "from-pass-env",
		"SECRET_ONLY": "secret",
		"PASS_ONLY":   "pass",
	}
	for key, want := range checks {
		if got := result[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestActiveSecretsMergesToolAndFeatureSecrets(t *testing.T) {
	r := &Runtime{
		profile: model.Profile{
			Name: "tool",
			Secrets: map[string]model.SecretConfig{
				"openai-api-key": secretConfig([]string{"OPENAI_API_KEY"}, nil),
			},
		},
		features: []model.Extension{{
			Name: "github-cli",
			Secrets: map[string]model.SecretConfig{
				"github-token": secretConfig([]string{"GH_TOKEN", "GITHUB_TOKEN"}, &model.HTTPSecretReleaseConfig{
					Hosts:  []string{"api.github.com"},
					Header: "authorization",
					Format: "Bearer %s",
				}),
			},
		}},
	}

	secrets, err := r.activeSecrets()
	if err != nil {
		t.Fatalf("activeSecrets() error = %v", err)
	}
	if len(secrets) != 2 {
		t.Fatalf("len(activeSecrets) = %d, want 2", len(secrets))
	}
	if got := secrets[0].ID; got != "github-token" {
		t.Fatalf("activeSecrets[0].ID = %q, want %q", got, "github-token")
	}
	if got := secrets[1].ID; got != "openai-api-key" {
		t.Fatalf("activeSecrets[1].ID = %q, want %q", got, "openai-api-key")
	}
	if got := strings.Join(secrets[0].EnvVars, ","); got != "GH_TOKEN,GITHUB_TOKEN" {
		t.Fatalf("github-token env vars = %q, want %q", got, "GH_TOKEN,GITHUB_TOKEN")
	}
}

func TestActiveSecretsRejectsEnvVarAliasCollision(t *testing.T) {
	r := &Runtime{
		profile: model.Profile{
			Name: "tool",
			Secrets: map[string]model.SecretConfig{
				"tool-secret": secretConfig([]string{"SHARED_TOKEN"}, nil),
			},
		},
		features: []model.Extension{{
			Name: "github-cli",
			Secrets: map[string]model.SecretConfig{
				"github-token": secretConfig([]string{"SHARED_TOKEN"}, nil),
			},
		}},
	}

	_, err := r.activeSecrets()
	if err == nil {
		t.Fatalf("activeSecrets() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "env var") {
		t.Fatalf("activeSecrets() error = %q, want env var collision", err)
	}
}

func TestActiveSecretsIncludesEnabledFeaturesAndExcludesDisabledFeatures(t *testing.T) {
	featureSecret := secretConfig([]string{"GH_TOKEN", "GITHUB_TOKEN"}, &model.HTTPSecretReleaseConfig{
		Hosts:  []string{"api.github.com"},
		Header: "authorization",
		Format: "Bearer %s",
	})

	rDisabled := &Runtime{
		profile: model.Profile{
			Name: "tool",
			Secrets: map[string]model.SecretConfig{
				"tool-secret": secretConfig([]string{"TOOL_SECRET"}, nil),
			},
		},
		features: nil,
	}
	disabledSecrets, err := rDisabled.activeSecrets()
	if err != nil {
		t.Fatalf("activeSecrets() with disabled feature error = %v", err)
	}
	if len(disabledSecrets) != 1 || disabledSecrets[0].ID != "tool-secret" {
		t.Fatalf("disabled feature secrets = %+v, want only tool-secret", disabledSecrets)
	}

	rEnabled := &Runtime{
		profile: rDisabled.profile,
		features: []model.Extension{{
			Name: "github-cli",
			Secrets: map[string]model.SecretConfig{
				"github-token": featureSecret,
			},
		}},
	}
	enabledSecrets, err := rEnabled.activeSecrets()
	if err != nil {
		t.Fatalf("activeSecrets() with enabled feature error = %v", err)
	}
	if len(enabledSecrets) != 2 {
		t.Fatalf("len(activeSecrets) with enabled feature = %d, want 2", len(enabledSecrets))
	}
	if enabledSecrets[0].ID != "github-token" || enabledSecrets[1].ID != "tool-secret" {
		t.Fatalf("enabled feature secrets = %+v, want github-token + tool-secret", enabledSecrets)
	}
}

func TestInjectDeclaredSecretsUsesPlaceholderAndRealHookValue(t *testing.T) {
	t.Setenv("API_KEY", "real-secret")

	r := runtimeWithProfile(t, model.Profile{
		Name: "tool",
		Providers: []model.ProviderConfig{{
			Name:              "provider",
			CredentialSecrets: []string{"api-key"},
		}},
		Secrets: map[string]model.SecretConfig{
			"api-key": secretConfig([]string{"API_KEY"}, &model.HTTPSecretReleaseConfig{
				Hosts:  []string{"api.example.com"},
				Header: "x-api-key",
			}),
		},
	})
	manager := newAuthManager(r)
	activeSecrets := mustActiveSecrets(t, r)

	env := []string{}
	hooks := &captureHooks{}
	injection, err := manager.injectDeclaredSecrets(hooks, authContextForRuntime(r), &env, map[string]string{}, map[string]string{}, nil, activeSecrets)
	if err != nil {
		t.Fatalf("injectDeclaredSecrets() error = %v", err)
	}

	value := envValue(env, "API_KEY")
	if value == "" {
		t.Fatalf("API_KEY was not injected")
	}
	if value == "real-secret" {
		t.Fatalf("API_KEY = %q, want placeholder", value)
	}
	if !strings.HasPrefix(value, "ENCLAVE_SECRET_") {
		t.Fatalf("API_KEY = %q, want placeholder prefix", value)
	}
	if hooks.afterInjected["API_KEY"] != "real-secret" {
		t.Fatalf("hook API_KEY = %q, want %q", hooks.afterInjected["API_KEY"], "real-secret")
	}
	if got := injection.SecretMapping.Entries[0].Value; got != "real-secret" {
		t.Fatalf("secret mapping value = %q, want %q", got, "real-secret")
	}
	if !injection.ProviderHasEnvCredential["provider"] {
		t.Fatalf("provider env credential flag = false, want true")
	}
}

func TestInjectDeclaredSecretsUsesPersistedFallbackForAliases(t *testing.T) {
	r := runtimeWithProfile(t, model.Profile{
		Name: "tool",
		Providers: []model.ProviderConfig{{
			Name:              "provider",
			CredentialSecrets: []string{"github-token"},
		}},
		Secrets: map[string]model.SecretConfig{
			"github-token": secretConfig([]string{"PRIMARY_TOKEN", "SECONDARY_TOKEN"}, nil),
		},
	})
	manager := newAuthManager(r)
	activeSecrets := mustActiveSecrets(t, r)

	env := []string{}
	injection, err := manager.injectDeclaredSecrets(
		stubHooks{},
		authContextForRuntime(r),
		&env,
		map[string]string{"PRIMARY_TOKEN": "persisted-token"},
		map[string]string{},
		nil,
		activeSecrets,
	)
	if err != nil {
		t.Fatalf("injectDeclaredSecrets() error = %v", err)
	}

	if got := envValue(env, "PRIMARY_TOKEN"); got != "persisted-token" {
		t.Fatalf("PRIMARY_TOKEN = %q, want %q", got, "persisted-token")
	}
	if got := envValue(env, "SECONDARY_TOKEN"); got != "persisted-token" {
		t.Fatalf("SECONDARY_TOKEN = %q, want %q", got, "persisted-token")
	}
	if got := injection.SecretValues["PRIMARY_TOKEN"]; got != "persisted-token" {
		t.Fatalf("SecretValues[PRIMARY_TOKEN] = %q, want %q", got, "persisted-token")
	}
	if got := injection.SecretValues["SECONDARY_TOKEN"]; got != "persisted-token" {
		t.Fatalf("SecretValues[SECONDARY_TOKEN] = %q, want %q", got, "persisted-token")
	}
	if !injection.ProviderHasEnvCredential["provider"] {
		t.Fatalf("provider env credential flag = false, want true")
	}
	if injection.SecretMapping.HasEntries() {
		t.Fatalf("secret mapping = %+v, want empty", injection.SecretMapping)
	}
}

func TestInjectDeclaredSecretsRejectsConflictingAliasValues(t *testing.T) {
	t.Setenv("FIRST_TOKEN", "first-token")

	r := runtimeWithProfile(t, model.Profile{
		Name: "tool",
		Secrets: map[string]model.SecretConfig{
			"github-token": secretConfig([]string{"FIRST_TOKEN", "SECOND_TOKEN"}, nil),
		},
	})
	manager := newAuthManager(r)
	activeSecrets := mustActiveSecrets(t, r)

	_, err := manager.injectDeclaredSecrets(
		stubHooks{},
		authContextForRuntime(r),
		&[]string{},
		map[string]string{"SECOND_TOKEN": "second-token"},
		map[string]string{},
		nil,
		activeSecrets,
	)
	if err == nil {
		t.Fatalf("injectDeclaredSecrets() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "conflicting values across env aliases") {
		t.Fatalf("injectDeclaredSecrets() error = %q, want alias conflict", err)
	}
}

func TestInjectDeclaredSecretsFallsBackToRealValueWhenPlaceholderFails(t *testing.T) {
	origResolver := newPlaceholderResolver
	newPlaceholderResolver = func() placeholderResolver { return failingPlaceholderResolver{} }
	defer func() { newPlaceholderResolver = origResolver }()

	t.Setenv("API_KEY", "real-secret")

	r := runtimeWithProfile(t, model.Profile{
		Name: "tool",
		Secrets: map[string]model.SecretConfig{
			"api-key": secretConfig([]string{"API_KEY"}, &model.HTTPSecretReleaseConfig{
				Hosts:  []string{"api.example.com"},
				Header: "x-api-key",
			}),
		},
	})
	manager := newAuthManager(r)
	activeSecrets := mustActiveSecrets(t, r)

	env := []string{}
	injection, err := manager.injectDeclaredSecrets(
		&captureHooks{},
		authContextForRuntime(r),
		&env,
		map[string]string{},
		map[string]string{},
		nil,
		activeSecrets,
	)
	if err != nil {
		t.Fatalf("injectDeclaredSecrets() error = %v", err)
	}

	if got := envValue(env, "API_KEY"); got != "real-secret" {
		t.Fatalf("API_KEY = %q, want %q", got, "real-secret")
	}
	if len(injection.SecretMapping.Entries) != 0 {
		t.Fatalf("secret mapping entries = %+v, want none", injection.SecretMapping.Entries)
	}
}

func TestInjectDeclaredSecretsOnlyInjectsDeclaredLayeredSecrets(t *testing.T) {
	home := t.TempDir()
	projectHash := "projhash"
	tool := "tool"

	setupSecretFiles(t, home, tool, projectHash, map[int]string{
		1: "DECLARED_KEY=declared\nUNDECLARED_KEY=should-not-leak\n",
	})

	layeredSecrets, err := auth.ResolveLayeredSecrets(home, projectHash, tool, model.SecretsScopeBoth)
	if err != nil {
		t.Fatalf("ResolveLayeredSecrets() error = %v", err)
	}

	r := runtimeWithProfile(t, model.Profile{
		Name: tool,
		Secrets: map[string]model.SecretConfig{
			"declared-key": secretConfig([]string{"DECLARED_KEY"}, nil),
		},
	})
	r.host.Home = home
	r.project.Hash = projectHash

	manager := newAuthManager(r)
	activeSecrets := mustActiveSecrets(t, r)

	env := []string{}
	injection, err := manager.injectDeclaredSecrets(
		stubHooks{},
		authContextForRuntime(r),
		&env,
		map[string]string{},
		layeredSecrets,
		nil,
		activeSecrets,
	)
	if err != nil {
		t.Fatalf("injectDeclaredSecrets() error = %v", err)
	}

	if got := envValue(env, "DECLARED_KEY"); got != "declared" {
		t.Fatalf("DECLARED_KEY = %q, want %q", got, "declared")
	}
	if got := envValue(env, "UNDECLARED_KEY"); got != "" {
		t.Fatalf("UNDECLARED_KEY = %q, want empty", got)
	}
	if _, ok := injection.SecretValues["UNDECLARED_KEY"]; ok {
		t.Fatalf("undeclared secret was injected: %+v", injection.SecretValues)
	}
}

func TestInjectDeclaredSecretsSuppressesOnlyProviderAPIKeySecrets(t *testing.T) {
	tests := []struct {
		name             string
		configure        func(*Runtime)
		wantAPIKeySecret bool
	}{
		{
			name: "--no-api-key",
			configure: func(r *Runtime) {
				r.auth.NoAPIKey = true
			},
			wantAPIKeySecret: false,
		},
		{
			name: "--ephemeral without --pass-api-key",
			configure: func(r *Runtime) {
				r.run.Ephemeral = true
			},
			wantAPIKeySecret: false,
		},
		{
			name: "--ephemeral with --pass-api-key",
			configure: func(r *Runtime) {
				r.run.Ephemeral = true
				r.auth.PassAPIKey = true
			},
			wantAPIKeySecret: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("API_KEY", "")
			t.Setenv("OAUTH_TOKEN", "")
			t.Setenv("GH_TOKEN", "")
			t.Setenv("GITHUB_TOKEN", "")

			oauthToken := secretConfig([]string{"OAUTH_TOKEN"}, nil)
			oauthToken.APIKey = boolPtr(false)
			r := runtimeWithProfile(t, model.Profile{
				Name: "tool",
				Providers: []model.ProviderConfig{{
					Name:              "provider",
					CredentialSecrets: []string{"api-key", "oauth-token"},
				}},
				Secrets: map[string]model.SecretConfig{
					"api-key": secretConfig([]string{"API_KEY"}, &model.HTTPSecretReleaseConfig{
						Hosts:  []string{"api.example.com"},
						Header: "x-api-key",
					}),
					"oauth-token": oauthToken,
				},
			})
			r.features = []model.Extension{{
				Name: "github-cli",
				Secrets: map[string]model.SecretConfig{
					"github-token": secretConfig([]string{"GH_TOKEN", "GITHUB_TOKEN"}, &model.HTTPSecretReleaseConfig{
						Hosts:  []string{"api.github.com"},
						Header: "authorization",
						Format: "Bearer %s",
					}),
				},
			}}
			tt.configure(r)

			manager := newAuthManager(r)
			activeSecrets := mustActiveSecrets(t, r)
			env := []string{}
			injection, err := manager.injectDeclaredSecrets(
				stubHooks{},
				authContextForRuntime(r),
				&env,
				map[string]string{},
				map[string]string{
					"API_KEY":     "api-secret",
					"OAUTH_TOKEN": "oauth-secret",
					"GH_TOKEN":    "github-secret",
				},
				nil,
				activeSecrets,
			)
			if err != nil {
				t.Fatalf("injectDeclaredSecrets() error = %v", err)
			}

			if got := envValue(env, "GH_TOKEN"); got == "" {
				t.Fatalf("GH_TOKEN was not injected")
			} else if got == "github-secret" {
				t.Fatalf("GH_TOKEN = %q, want secret-release placeholder", got)
			}
			if got := envValue(env, "GITHUB_TOKEN"); got == "" {
				t.Fatalf("GITHUB_TOKEN was not injected")
			}
			if got := injection.SecretValues["GH_TOKEN"]; got != "github-secret" {
				t.Fatalf("SecretValues[GH_TOKEN] = %q, want %q", got, "github-secret")
			}
			if got := injection.SecretValues["GITHUB_TOKEN"]; got != "github-secret" {
				t.Fatalf("SecretValues[GITHUB_TOKEN] = %q, want %q", got, "github-secret")
			}
			if !injection.ResolvedSecretIDs["github-token"] {
				t.Fatalf("github-token was not marked resolved")
			}
			if !secretMappingHasID(injection.SecretMapping, "github-token") {
				t.Fatalf("github-token missing from secret release mapping: %+v", injection.SecretMapping.Entries)
			}
			if got := envValue(env, "OAUTH_TOKEN"); got != "oauth-secret" {
				t.Fatalf("OAUTH_TOKEN = %q, want %q", got, "oauth-secret")
			}
			if got := injection.SecretValues["OAUTH_TOKEN"]; got != "oauth-secret" {
				t.Fatalf("SecretValues[OAUTH_TOKEN] = %q, want %q", got, "oauth-secret")
			}
			if !injection.ResolvedSecretIDs["oauth-token"] {
				t.Fatalf("oauth-token was not marked resolved")
			}

			if !injection.ProviderHasEnvCredential["provider"] {
				t.Fatalf("ProviderHasEnvCredential = false, want true")
			}
			if tt.wantAPIKeySecret {
				if got := envValue(env, "API_KEY"); got == "" {
					t.Fatalf("API_KEY was not injected")
				} else if got == "api-secret" {
					t.Fatalf("API_KEY = %q, want secret-release placeholder", got)
				}
				if got := injection.SecretValues["API_KEY"]; got != "api-secret" {
					t.Fatalf("SecretValues[API_KEY] = %q, want %q", got, "api-secret")
				}
				if !injection.ResolvedSecretIDs["api-key"] {
					t.Fatalf("api-key was not marked resolved")
				}
				if !secretMappingHasID(injection.SecretMapping, "api-key") {
					t.Fatalf("api-key missing from secret release mapping: %+v", injection.SecretMapping.Entries)
				}
			} else {
				if got := envValue(env, "API_KEY"); got != "" {
					t.Fatalf("API_KEY = %q, want empty", got)
				}
				if _, ok := injection.SecretValues["API_KEY"]; ok {
					t.Fatalf("API_KEY should not be in SecretValues: %+v", injection.SecretValues)
				}
				if injection.ResolvedSecretIDs["api-key"] {
					t.Fatalf("api-key should not be marked resolved")
				}
				if secretMappingHasID(injection.SecretMapping, "api-key") {
					t.Fatalf("api-key should not be in secret release mapping: %+v", injection.SecretMapping.Entries)
				}
			}
		})
	}
}

func TestInjectDeclaredSecretsUsesDistinctGitLabReleaseShapes(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "pat-secret")
	t.Setenv("OAUTH_TOKEN", "oauth-secret")
	t.Setenv("CI_JOB_TOKEN", "job-secret")

	r := runtimeWithProfile(t, model.Profile{
		Name: "tool",
		Secrets: map[string]model.SecretConfig{
			"tool-secret": secretConfig([]string{"TOOL_SECRET"}, nil),
		},
	})
	r.features = []model.Extension{{
		Name: "gitlab-cli",
		Secrets: map[string]model.SecretConfig{
			"gitlab-token": secretConfig([]string{"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN"}, &model.HTTPSecretReleaseConfig{
				Hosts:  []string{"gitlab.com", "*.gitlab.com"},
				Header: "private-token",
			}),
			"gitlab-oauth-token": secretConfig([]string{"OAUTH_TOKEN"}, &model.HTTPSecretReleaseConfig{
				Hosts:  []string{"gitlab.com", "*.gitlab.com"},
				Header: "authorization",
				Format: "Bearer %s",
			}),
			"gitlab-job-token": secretConfig([]string{"JOB_TOKEN", "CI_JOB_TOKEN"}, &model.HTTPSecretReleaseConfig{
				Hosts:  []string{"gitlab.com", "*.gitlab.com"},
				Header: "job-token",
			}),
		},
	}}

	manager := newAuthManager(r)
	activeSecrets := mustActiveSecrets(t, r)
	env := []string{}
	injection, err := manager.injectDeclaredSecrets(
		stubHooks{},
		authContextForRuntime(r),
		&env,
		map[string]string{},
		map[string]string{},
		nil,
		activeSecrets,
	)
	if err != nil {
		t.Fatalf("injectDeclaredSecrets() error = %v", err)
	}

	if len(injection.SecretMapping.Entries) != 3 {
		t.Fatalf("len(secret mapping entries) = %d, want 3", len(injection.SecretMapping.Entries))
	}
	entriesByID := map[string]model.SecretReleaseEntry{}
	for _, entry := range injection.SecretMapping.Entries {
		entriesByID[entry.SecretID] = entry
	}

	if got := entriesByID["gitlab-token"].Header; got != "private-token" {
		t.Fatalf("gitlab-token header = %q, want %q", got, "private-token")
	}
	if got := entriesByID["gitlab-token"].Format; got != "" {
		t.Fatalf("gitlab-token format = %q, want empty", got)
	}
	if got := entriesByID["gitlab-oauth-token"].Header; got != "authorization" {
		t.Fatalf("gitlab-oauth-token header = %q, want %q", got, "authorization")
	}
	if got := entriesByID["gitlab-oauth-token"].Format; got != "Bearer %s" {
		t.Fatalf("gitlab-oauth-token format = %q, want %q", got, "Bearer %s")
	}
	if got := entriesByID["gitlab-job-token"].Header; got != "job-token" {
		t.Fatalf("gitlab-job-token header = %q, want %q", got, "job-token")
	}
	if got := entriesByID["gitlab-job-token"].Format; got != "" {
		t.Fatalf("gitlab-job-token format = %q, want empty", got)
	}

	if envValue(env, "GITLAB_TOKEN") == "" || envValue(env, "GITLAB_ACCESS_TOKEN") == "" {
		t.Fatalf("gitlab PAT aliases were not both injected: %v", env)
	}
	if envValue(env, "JOB_TOKEN") == "" || envValue(env, "CI_JOB_TOKEN") == "" {
		t.Fatalf("gitlab job token aliases were not both injected: %v", env)
	}
	if envValue(env, "GITLAB_TOKEN") == "pat-secret" || envValue(env, "OAUTH_TOKEN") == "oauth-secret" || envValue(env, "CI_JOB_TOKEN") == "job-secret" {
		t.Fatalf("gitlab env values should be placeholders when release is enabled: %v", env)
	}
}

func TestInjectDeclaredSecretsProxyManagedRestrictsPlaceholderToListedVars(t *testing.T) {
	t.Setenv("PRIMARY_TOKEN", "real-secret")
	t.Setenv("SECONDARY_TOKEN", "real-secret")

	r := runtimeWithProfile(t, model.Profile{
		Name:         "tool",
		ProxyManaged: []string{"PRIMARY_TOKEN"},
		Secrets: map[string]model.SecretConfig{
			"api-key": secretConfig([]string{"PRIMARY_TOKEN", "SECONDARY_TOKEN"}, &model.HTTPSecretReleaseConfig{
				Hosts:  []string{"api.example.com"},
				Header: "x-api-key",
			}),
		},
	})
	manager := newAuthManager(r)
	activeSecrets := mustActiveSecrets(t, r)

	env := []string{}
	injection, err := manager.injectDeclaredSecrets(
		stubHooks{},
		authContextForRuntime(r),
		&env,
		map[string]string{},
		map[string]string{},
		nil,
		activeSecrets,
	)
	if err != nil {
		t.Fatalf("injectDeclaredSecrets() error = %v", err)
	}

	// The proxyManaged alias carries the placeholder...
	if got := envValue(env, "PRIMARY_TOKEN"); !strings.HasPrefix(got, "ENCLAVE_SECRET_") {
		t.Fatalf("PRIMARY_TOKEN = %q, want secret-release placeholder", got)
	}
	// ...while the sibling alias, absent from proxyManaged, keeps the raw value.
	if got := envValue(env, "SECONDARY_TOKEN"); got != "real-secret" {
		t.Fatalf("SECONDARY_TOKEN = %q, want raw value %q", got, "real-secret")
	}
	// The real value is still persisted and handed to hooks for both aliases.
	if got := injection.SecretValues["PRIMARY_TOKEN"]; got != "real-secret" {
		t.Fatalf("SecretValues[PRIMARY_TOKEN] = %q, want %q", got, "real-secret")
	}
	if got := injection.SecretValues["SECONDARY_TOKEN"]; got != "real-secret" {
		t.Fatalf("SecretValues[SECONDARY_TOKEN] = %q, want %q", got, "real-secret")
	}
	// A single release entry is registered so the proxy can swap the placeholder.
	if !secretMappingHasID(injection.SecretMapping, "api-key") {
		t.Fatalf("api-key missing from secret release mapping: %+v", injection.SecretMapping.Entries)
	}
}

func TestShouldUseSecretReleases(t *testing.T) {
	activeSecrets := []activeSecret{{
		ID:          "api-key",
		EnvVars:     []string{"API_KEY"},
		ReleaseHTTP: &model.HTTPSecretReleaseConfig{Hosts: []string{"api.example.com"}, Header: "x-api-key"},
	}}

	t.Run("disabled without any release policy", func(t *testing.T) {
		r := runtimeWithProfile(t, model.Profile{Name: "tool"})
		if got := newAuthManager(r).shouldUseSecretReleases(nil); got {
			t.Fatalf("shouldUseSecretReleases(nil) = true, want false")
		}
	})

	t.Run("disabled when policy mode is unrestricted", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		home := t.TempDir()
		policyPath := config.HostNetworkPolicyPath(home)
		if err := os.MkdirAll(filepath.Dir(policyPath), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(policyPath, []byte(`{"mode":"unrestricted"}`), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		r := runtimeWithProfile(t, model.Profile{Name: "tool"})
		r.host.Home = home
		if got := newAuthManager(r).shouldUseSecretReleases(activeSecrets); got {
			t.Fatalf("shouldUseSecretReleases() = true, want false")
		}
	})

	t.Run("enabled when network is restricted", func(t *testing.T) {
		r := runtimeWithProfile(t, model.Profile{Name: "tool"})
		if got := newAuthManager(r).shouldUseSecretReleases(activeSecrets); !got {
			t.Fatalf("shouldUseSecretReleases() = false, want true")
		}
	})
}

func TestBuildPersistedEnvPersistsDeclaredSecrets(t *testing.T) {
	env := mergeEnvForPersist(
		map[string]string{"EXISTING": "value"},
		map[string]string{"API_KEY": "real-secret", "SECOND_KEY": "second-secret"},
		map[string]string{"PASS_ENV": "pass-value"},
	)

	if got := env["API_KEY"]; got != "real-secret" {
		t.Fatalf("API_KEY = %q, want %q", got, "real-secret")
	}
	if got := env["SECOND_KEY"]; got != "second-secret" {
		t.Fatalf("SECOND_KEY = %q, want %q", got, "second-secret")
	}
	if got := env["PASS_ENV"]; got != "pass-value" {
		t.Fatalf("PASS_ENV = %q, want %q", got, "pass-value")
	}
	if got := env["EXISTING"]; got != "value" {
		t.Fatalf("EXISTING = %q, want %q", got, "value")
	}
}

func runtimeWithProfile(t *testing.T, profile model.Profile) *Runtime {
	t.Helper()
	if profile.Name == "" {
		profile.Name = "tool"
	}
	return &Runtime{
		host:    model.Host{Home: t.TempDir()},
		project: model.Project{Hash: "projhash"},
		profile: profile,
		auth:    model.AuthOptions{SecretsScope: model.SecretsScopeProject},
		run:     model.RunOptions{},
	}
}

func authContextForRuntime(r *Runtime) auth.Context {
	return auth.Context{
		Host:    r.host,
		Project: r.project,
		Profile: r.profile,
		Run:     r.run,
		Auth:    r.auth,
		Build:   r.build,
	}
}

func secretConfig(envVars []string, release *model.HTTPSecretReleaseConfig) model.SecretConfig {
	cfg := model.SecretConfig{
		EnvVars: append([]string{}, envVars...),
	}
	if release != nil {
		cfg.Release = &model.SecretReleaseConfig{
			HTTP: &model.HTTPSecretReleaseConfig{
				Hosts:  append([]string{}, release.Hosts...),
				Header: release.Header,
				Format: release.Format,
			},
		}
	}
	return cfg
}

func mustActiveSecrets(t *testing.T, r *Runtime) []activeSecret {
	t.Helper()
	secrets, err := r.activeSecrets()
	if err != nil {
		t.Fatalf("activeSecrets() error = %v", err)
	}
	return secrets
}

func setupSecretFiles(t *testing.T, home, tool, projectHash string, layers map[int]string) {
	t.Helper()
	for layer, content := range layers {
		var path string
		switch layer {
		case 1:
			path = config.HostSecretsGlobalSharedFile(home)
		case 2:
			path = config.HostSecretsGlobalFile(home, tool)
		case 3:
			path = config.HostSecretsProjectFile(home, projectHash, tool)
		default:
			t.Fatalf("unknown secret layer %d", layer)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return entry[len(prefix):]
		}
	}
	return ""
}

func secretMappingHasID(mapping SecretMapping, secretID string) bool {
	for _, entry := range mapping.Entries {
		if entry.SecretID == secretID {
			return true
		}
	}
	return false
}
