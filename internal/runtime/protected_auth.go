// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"enclave/internal/auth"
	"enclave/internal/backend"
	"enclave/internal/model"
	"enclave/internal/util"
)

func (r *Runtime) validateProtectedAuth() error {
	if !r.auth.ProtectAuth {
		return nil
	}
	if r.profile.Name != "claude" || r.profile.Command != "claude" || r.profile.ConfigDir != ".claude" {
		return fmt.Errorf("protected auth requires the standard Claude profile")
	}
	if r.backend == nil || (r.backend.Name() != backend.NameDocker && r.backend.Name() != backend.NamePodman) {
		return fmt.Errorf("protected auth requires Docker or Podman")
	}
	if r.host.UID == "" || r.host.UID == "0" || r.host.GID == "" ||
		(r.build.BuildUID != "" && r.build.BuildUID != r.host.UID) ||
		(r.build.BuildGID != "" && r.build.BuildGID != r.host.GID) {
		return fmt.Errorf("protected auth requires the non-root host UID/GID as the image and workload identity")
	}
	if !r.run.Persist || r.run.Ephemeral || r.auth.AuthScope != model.AuthScopeShared || r.auth.AuthName == "" {
		return fmt.Errorf("protected auth requires a named shared identity and persistence")
	}
	switch {
	case r.devcontainer != nil || r.build.Devcontainer:
		return fmt.Errorf("protected auth does not support devcontainers (--devcontainer)")
	case r.run.Admin:
		return fmt.Errorf("protected auth does not support admin mode (--admin)")
	case r.build.RuntimeUIDRemap:
		return fmt.Errorf("protected auth does not support runtime UID remapping (--runtime-uid-remap)")
	case r.run.HostConfig != "" && r.run.HostConfig != model.HostConfigNone:
		return fmt.Errorf("protected auth does not support host config passthrough; use --host-config=none")
	case r.auth.PassAPIKey:
		return fmt.Errorf("protected auth does not support API key injection (--pass-api-key)")
	case r.build.BaseImage != "":
		return fmt.Errorf("protected auth does not support custom base images (--base-image)")
	}
	for _, feature := range r.features {
		if feature.Name == "github-cli" || (feature.ConfigDir != "" && len(feature.AuthFiles) != 0) {
			files := slices.Clone(feature.AuthFiles)
			slices.Sort(files)
			if feature.Name != "github-cli" || feature.ConfigDir != ".config/gh" || !slices.Equal(files, []string{"config.yml", "hosts.yml"}) {
				return fmt.Errorf("protected auth supports only the standard github-cli native-auth feature; unsupported feature %s", feature.Name)
			}
		}
	}
	for _, arg := range r.run.CmdArgs {
		for _, option := range []string{"--settings", "--setting-sources", "--plugin-dir", "--mcp-config"} {
			if arg == option || strings.HasPrefix(arg, option+"=") {
				return fmt.Errorf("protected auth does not support %s overrides", option)
			}
		}
	}
	for _, key := range r.auth.PassEnv {
		if protectedCredentialEnv(key) || protectedBindingEnv(key) || (r.protectedGitHubEnabled() && protectedGitHubEnv(key)) {
			return fmt.Errorf("protected auth conflicts with --pass-env %s", key)
		}
	}
	return nil
}

func (r *Runtime) protectedGitHubEnabled() bool {
	return r.auth.ProtectAuth && slices.ContainsFunc(r.features, func(feature model.Extension) bool {
		return feature.Name == "github-cli" && feature.ConfigDir == ".config/gh" && len(feature.AuthFiles) != 0
	})
}

func protectedGitHubEnv(key string) bool {
	switch key {
	case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_CONFIG_DIR", model.EnvProtectedGitHub:
		return true
	}
	return false
}

func protectedCredentialEnv(key string) bool {
	if strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE_CODE_OAUTH") || strings.HasPrefix(key, "CLAUDE_CODE_CUSTOM_OAUTH") || strings.HasPrefix(key, "CLAUDE_CODE_USE_") || strings.HasPrefix(key, "CLAUDE_CODE_SKIP_") || strings.HasPrefix(key, "CLAUDE_CODE_HOST_") {
		return true
	}
	switch key {
	case "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST", "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR", "CLAUDE_CODE_SESSION_ACCESS_TOKEN", "CLAUDE_CODE_WEBSOCKET_AUTH_FILE_DESCRIPTOR", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy":
		return true
	}
	return false
}

func protectedBindingEnv(key string) bool {
	switch key {
	case "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR", model.EnvAuthDir, model.EnvAuthFiles, model.EnvToolConfigDir, "ENCLAVE_PROTECTED_AUTH":
		return true
	}
	return false
}

func validateProtectedEnv(env []string) error {
	for _, assignment := range env {
		key, value, _ := strings.Cut(assignment, "=")
		if value != "" && protectedCredentialEnv(key) {
			return fmt.Errorf("protected auth conflicts with environment setting %s", key)
		}
	}
	return nil
}

func (r *Runtime) validateProtectedBindings(env []string) error {
	view := resolveContainerProfilePath(r.containerHome, r.profile.ConfigDir)
	for _, assignment := range env {
		key, value, _ := strings.Cut(assignment, "=")
		if r.protectedGitHubEnabled() && protectedGitHubEnv(key) {
			want := ""
			switch key {
			case "GH_CONFIG_DIR":
				want = r.containerHome + "/.config/gh"
			case "GH_HOST":
				want = "github.com"
			case model.EnvProtectedGitHub:
				want = "1"
			}
			if value != want {
				return fmt.Errorf("protected GitHub conflicts with environment setting %s", key)
			}
		}
		if !protectedBindingEnv(key) {
			continue
		}
		want := view
		if key == model.EnvAuthFiles {
			want = ""
		}
		if key == "ENCLAVE_PROTECTED_AUTH" {
			want = "1"
		}
		if value != want {
			return fmt.Errorf("protected auth conflicts with native auth location setting %s", key)
		}
	}
	return validateProtectedEnv(env)
}

func (m authManager) prepareProtected(env *[]string, stores storeSet) (model.AuthState, SecretMapping, error) {
	// Never run the tool's legacy import, API-key or native-auth hooks here.
	persisted := map[string]string{}
	if stores.Env != nil {
		if err := m.withStoreLock(stores.Env, func() error {
			data, err := m.storage().ReadFile(context.Background(), stores.Env.Key, stores.Env.Kind, "env")
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			persisted, err = util.ParseEnv(bytes.NewReader(data))
			return err
		}); err != nil {
			return model.AuthState{}, SecretMapping{}, fmt.Errorf("cannot validate protected session's persisted environment")
		}
	}
	layers, err := auth.ResolveSecretsLayers(m.host.Home, m.project.Hash, m.profile.Name, m.auth.SecretsScope)
	if err != nil {
		return model.AuthState{}, SecretMapping{}, fmt.Errorf("read protected session secret configuration: %w", err)
	}
	secrets, err := m.activeSecrets()
	if err != nil {
		return model.AuthState{}, SecretMapping{}, err
	}
	var allowed []activeSecret
	for _, secret := range secrets {
		providerSecret := false
		for _, key := range secret.EnvVars {
			providerSecret = providerSecret || protectedCredentialEnv(key) || protectedBindingEnv(key) || (m.protectedGitHubEnabled() && protectedGitHubEnv(key))
		}
		if !providerSecret {
			allowed = append(allowed, secret)
			continue
		}
		if _, _, found, err := resolveActiveSecretValue(secret, m.host.Home, layers, persisted); err != nil {
			return model.AuthState{}, SecretMapping{}, fmt.Errorf("resolve protected session credential conflict: %w", err)
		} else if found {
			return model.AuthState{}, SecretMapping{}, fmt.Errorf("protected auth conflicts with configured credential %s; use a separate secret scope without provider credentials", secret.ID)
		}
	}
	for key, value := range persisted {
		if value != "" && (protectedCredentialEnv(key) || protectedBindingEnv(key) || (m.protectedGitHubEnabled() && protectedGitHubEnv(key))) {
			return model.AuthState{}, SecretMapping{}, fmt.Errorf("protected auth conflicts with persisted environment setting %s", key)
		}
	}
	for _, assignment := range os.Environ() {
		key, value, _ := strings.Cut(assignment, "=")
		// Unforwarded host proxy settings do not affect the workload.
		if value != "" && (strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE_CODE_OAUTH") || strings.HasPrefix(key, "CLAUDE_CODE_USE_") || (m.protectedGitHubEnabled() && protectedGitHubEnv(key))) {
			return model.AuthState{}, SecretMapping{}, fmt.Errorf("protected auth conflicts with host environment setting %s", key)
		}
	}
	if m.configSourceDir != "" {
		for _, name := range []string{".credentials.json", "config.json", ".claude.json", ".config.json"} {
			if _, err := os.Lstat(filepath.Join(m.configSourceDir, name)); err == nil || !os.IsNotExist(err) {
				return model.AuthState{}, SecretMapping{}, fmt.Errorf("protected auth refuses native auth file in config source: %s", name)
			}
		}
		if err := validateProtectedSettings(filepath.Join(m.configSourceDir, "settings.json"), m.protectedGitHubEnabled()); err != nil {
			return model.AuthState{}, SecretMapping{}, err
		}
	}
	ctx := auth.Context{Host: m.host, Project: m.project, Profile: m.profile, Run: m.run, Auth: m.auth, Build: m.build, Storage: m.storage(), ConfigStore: stores.Config}
	injection, err := m.injectDeclaredSecrets(auth.HooksFor(model.Profile{}), ctx, env, persisted, layers, nil, allowed)
	if err != nil {
		return model.AuthState{}, SecretMapping{}, err
	}
	passed := m.injectPassEnv(env, persisted, injection.InjectedKeys)
	if err := m.validateProtectedBindings(*env); err != nil {
		return model.AuthState{}, SecretMapping{}, err
	}
	m.persistEnvToStore(stores, persisted, injection.SecretValues, passed)
	return model.AuthState{Providers: map[string]model.ProviderAuthState{"anthropic": {HasSession: true}}}, injection.SecretMapping, nil
}

func validateProtectedSettings(filename string, github bool) error {
	data, err := os.ReadFile(filename) // #nosec G304 -- fixed settings.json in the host-composed tool config source, never an HTTP input.
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read protected Claude settings: %w", err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("protected auth requires valid Claude settings")
	}
	for _, key := range []string{"apiKeyHelper", "forceLoginMethod", "forceLoginOrgUUID", "awsAuthRefresh", "awsCredentialExport"} {
		if _, exists := settings[key]; exists {
			return fmt.Errorf("protected auth conflicts with Claude setting %s", key)
		}
	}
	if raw, ok := settings["env"]; ok {
		var env map[string]string
		if err := json.Unmarshal(raw, &env); err != nil {
			return fmt.Errorf("protected auth requires string-valued settings environment")
		}
		for key := range env {
			if protectedCredentialEnv(key) || protectedBindingEnv(key) || (github && protectedGitHubEnv(key)) {
				return fmt.Errorf("protected auth conflicts with Claude settings environment %s", key)
			}
		}
	}
	return nil
}
