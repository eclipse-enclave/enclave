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
	"enclave/internal/protectedauth"
	"enclave/internal/util"
)

func (r *Runtime) validateProtectedAuth() error {
	if !r.auth.ProtectAuth {
		return nil
	}
	tool, known := protectedauth.Lookup(r.profile.Name)
	if !known || r.profile.ConfigDir != tool.ConfigDir {
		return fmt.Errorf("protected auth does not support tool %s", r.profile.Name)
	}
	if r.backend == nil || (r.backend.Name() != backend.NameDocker && r.backend.Name() != backend.NamePodman) {
		return fmt.Errorf("protected auth requires Docker or Podman")
	}
	if r.host.UID == "" || r.host.UID == "0" || r.host.GID == "" ||
		(r.build.BuildUID != "" && r.build.BuildUID != r.host.UID) ||
		(r.build.BuildGID != "" && r.build.BuildGID != r.host.GID) {
		return fmt.Errorf("protected auth requires the non-root host UID/GID as the image and workload identity")
	}
	if r.run.Persist && r.auth.AuthScope == model.AuthScopeShared && r.auth.AuthName == "" && len(tool.Adapters) != 0 {
		return fmt.Errorf("protected auth with the shared scope requires a named identity (--auth-name)")
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
		if r.profile.Name != "claude" {
			break
		}
		for _, option := range []string{"--settings", "--setting-sources", "--plugin-dir", "--mcp-config"} {
			if arg == option || strings.HasPrefix(arg, option+"=") {
				return fmt.Errorf("protected auth does not support %s overrides", option)
			}
		}
	}
	return r.validateProtectedPassEnv()
}

func (r *Runtime) validateProtectedPassEnv() error {
	for _, key := range r.auth.PassEnv {
		if protectedCredentialEnv(key) || r.protectedCopilotTokenEnv(key) || r.protectedBindingEnv(key) || (r.protectedGitHubEnabled() && protectedGitHubEnv(key)) {
			return fmt.Errorf("protected auth conflicts with --pass-env %s", key)
		}
	}
	return nil
}

// protectedAuthIdentity selects the gateway-owned identity of a protected run:
// a fresh one for a non-persistent session, the project's for the project
// scope, or the named shared identity.
func (r *Runtime) protectedAuthIdentity() (string, error) {
	if r.protectedIdentity != "" {
		return r.protectedIdentity, nil
	}
	tool, _ := protectedauth.Lookup(r.profile.Name)
	switch {
	// A tool without login adapters holds no grant worth keeping.
	case !r.run.Persist || len(tool.Adapters) == 0:
		name, err := backend.NewProtectedEphemeralIdentity()
		if err != nil {
			return "", err
		}
		r.protectedIdentity = name
	case r.auth.AuthScope == model.AuthScopeProject:
		r.protectedIdentity = backend.ProtectedProjectIdentity(r.project.Hash)
	default:
		r.protectedIdentity = r.auth.AuthName
	}
	return r.protectedIdentity, nil
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

func (r *Runtime) protectedCopilotTokenEnv(key string) bool {
	if r.profile.Name != "copilot" {
		return false
	}
	switch key {
	case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN":
		return true
	}
	return false
}

func protectedCredentialEnv(key string) bool {
	if strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE_CODE_OAUTH") || strings.HasPrefix(key, "CLAUDE_CODE_CUSTOM_OAUTH") || strings.HasPrefix(key, "CLAUDE_CODE_USE_") || strings.HasPrefix(key, "CLAUDE_CODE_SKIP_") || strings.HasPrefix(key, "CLAUDE_CODE_HOST_") {
		return true
	}
	switch key {
	case "OPENAI_API_KEY", "CODEX_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "MISTRAL_API_KEY", "COPILOT_GITHUB_TOKEN", "GITHUB_COPILOT_API_TOKEN",
		"CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST", "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR", "CLAUDE_CODE_SESSION_ACCESS_TOKEN", "CLAUDE_CODE_WEBSOCKET_AUTH_FILE_DESCRIPTOR", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy":
		return true
	}
	return false
}

// protectedBindingEnv names variables that locate the tool's credentials. The
// runtime sets the ones it needs; any other value would move credentials away
// from the gateway's view.
func (r *Runtime) protectedBindingEnv(key string) bool {
	switch key {
	case model.EnvAuthDir, model.EnvAuthFiles, model.EnvToolConfigDir, model.EnvProtectedAuth, model.EnvProtectedAuthFiles:
		return true
	}
	tool, _ := protectedauth.Lookup(r.profile.Name)
	return slices.Contains(tool.LocationEnv, key)
}

// sessionPlaceholders lists the placeholders the gateway releases in this
// session. A credential variable may carry only one of them.
func sessionPlaceholders(mapping SecretMapping) map[string]bool {
	placeholders := map[string]bool{}
	for _, entry := range mapping.Entries {
		placeholders[entry.Placeholder] = true
	}
	return placeholders
}

func validateProtectedEnv(env []string, placeholders map[string]bool) error {
	for _, assignment := range env {
		key, value, _ := strings.Cut(assignment, "=")
		if value != "" && protectedCredentialEnv(key) && !placeholders[value] {
			return fmt.Errorf("protected auth conflicts with environment setting %s; configure it as an Enclave secret", key)
		}
	}
	return nil
}

func (r *Runtime) validateProtectedBindings(env []string, placeholders map[string]bool) error {
	configDir := resolveContainerProfilePath(r.containerHome, r.profile.ConfigDir)
	view := r.containerHome + "/" + model.ContainerAuthDir
	tool, _ := protectedauth.Lookup(r.profile.Name)
	for _, assignment := range env {
		key, value, _ := strings.Cut(assignment, "=")
		if r.protectedCopilotTokenEnv(key) && value != "" && !placeholders[value] {
			return fmt.Errorf("protected Copilot auth conflicts with environment setting %s", key)
		}
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
		if !r.protectedBindingEnv(key) {
			continue
		}
		want := ""
		switch key {
		case model.EnvAuthDir, tool.StorageDirEnv:
			want = view
		case "CLAUDE_CONFIG_DIR", model.EnvToolConfigDir:
			want = configDir
		case model.EnvProtectedAuth:
			want = "1"
		case model.EnvProtectedAuthFiles:
			want = tool.LinkSpec()
		}
		if value != want {
			return fmt.Errorf("protected auth conflicts with native auth location setting %s", key)
		}
	}
	return validateProtectedEnv(env, placeholders)
}

func (m authManager) prepareProtected(env *[]string, stores storeSet) (model.AuthState, SecretMapping, error) {
	// Never run the tool's legacy import, API-key or native-auth hooks here.
	if err := m.validateProtectedPassEnv(); err != nil {
		return model.AuthState{}, SecretMapping{}, err
	}
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
	// Declared credentials reach the workload only as placeholders the gateway
	// releases at their declared hosts and header. Host selectors name an
	// instance, not a credential, and stay plain.
	selectors := hostCredentialRefs(secrets)
	var allowed []activeSecret
	for _, secret := range secrets {
		binding := false
		for _, key := range secret.EnvVars {
			binding = binding || m.protectedBindingEnv(key) || (m.protectedGitHubEnabled() && protectedGitHubEnv(key))
		}
		_, selector := selectors[secret.ID]
		if !binding && (secret.ReleaseHTTP != nil || selector) {
			allowed = append(allowed, secret)
			continue
		}
		if _, _, found, err := resolveActiveSecretValue(secret, m.host.Home, layers, persisted); err != nil {
			return model.AuthState{}, SecretMapping{}, fmt.Errorf("resolve protected session credential conflict: %w", err)
		} else if found && binding {
			return model.AuthState{}, SecretMapping{}, fmt.Errorf("protected auth conflicts with configured credential %s; use a separate secret scope without it", secret.ID)
		} else if found {
			return model.AuthState{}, SecretMapping{}, fmt.Errorf("protected auth cannot pass credential %s without a gateway release rule", secret.ID)
		}
	}
	for key, value := range persisted {
		if value != "" && (m.protectedBindingEnv(key) || (m.protectedGitHubEnabled() && protectedGitHubEnv(key))) {
			return model.AuthState{}, SecretMapping{}, fmt.Errorf("protected auth conflicts with persisted environment setting %s", key)
		}
	}
	for _, assignment := range os.Environ() {
		key, value, _ := strings.Cut(assignment, "=")
		// Unforwarded host proxy settings do not affect the workload.
		if value != "" && m.protectedGitHubEnabled() && protectedGitHubEnv(key) {
			return model.AuthState{}, SecretMapping{}, fmt.Errorf("protected auth conflicts with host environment setting %s", key)
		}
	}
	if m.configSourceDir != "" {
		names := append(m.profile.RuntimeAuthFiles(), ".config.json")
		tool, _ := protectedauth.Lookup(m.profile.Name)
		for _, file := range tool.Files {
			names = append(names, file.ConfigPath)
		}
		for _, name := range names {
			if _, err := os.Lstat(filepath.Join(m.configSourceDir, filepath.FromSlash(name))); err == nil || !os.IsNotExist(err) {
				return model.AuthState{}, SecretMapping{}, fmt.Errorf("protected auth refuses native auth file in config source: %s", name)
			}
		}
		if m.profile.Name == "claude" {
			if err := m.validateProtectedSettings(filepath.Join(m.configSourceDir, "settings.json"), m.protectedGitHubEnabled()); err != nil {
				return model.AuthState{}, SecretMapping{}, err
			}
		}
	}
	ctx := auth.Context{Host: m.host, Project: m.project, Profile: m.profile, Run: m.run, Auth: m.auth, Build: m.build, Storage: m.storage(), ConfigStore: stores.Config}
	injection, err := m.injectDeclaredSecrets(auth.HooksFor(model.Profile{}), ctx, env, persisted, layers, nil, allowed)
	if err != nil {
		return model.AuthState{}, SecretMapping{}, err
	}
	passed := m.injectPassEnv(env, persisted, injection.InjectedKeys, injection.SuppressedEnvVars)
	if err := m.validateProtectedBindings(*env, sessionPlaceholders(injection.SecretMapping)); err != nil {
		return model.AuthState{}, SecretMapping{}, err
	}
	m.persistEnvToStore(stores, persisted, injection.SecretValues, passed)
	return m.protectedAuthState(injection.ProviderHasEnvCredential), injection.SecretMapping, nil
}

// protectedAuthState reports a provider session where admission found a ready
// grant of the adapter that captures its login, so later sessions skip login
// callback ports as ordinary ones do.
func (m authManager) protectedAuthState(envCredential map[string]bool) model.AuthState {
	tool, _ := protectedauth.Lookup(m.profile.Name)
	var ready []string
	if m.authSession != nil {
		ready = m.authSession.Logins
	}
	state := model.AuthState{Providers: map[string]model.ProviderAuthState{}}
	for _, provider := range m.profile.Providers {
		if provider.Name == "" {
			continue
		}
		adapter, captured := tool.Providers[provider.Name]
		state.Providers[provider.Name] = model.ProviderAuthState{
			HasEnvCredential: envCredential[provider.Name],
			HasSession:       captured && slices.Contains(ready, adapter),
		}
	}
	return state
}

func (r *Runtime) validateProtectedSettings(filename string, github bool) error {
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
			if protectedCredentialEnv(key) || r.protectedBindingEnv(key) || (github && protectedGitHubEnv(key)) {
				return fmt.Errorf("protected auth conflicts with Claude settings environment %s", key)
			}
		}
	}
	return nil
}
