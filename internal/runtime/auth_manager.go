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
	"fmt"
	"os"

	"enclave/internal/auth"
	"enclave/internal/backend"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

type authManager struct {
	*Runtime
}

type placeholderResolver interface {
	ResolvePlaceholder(string) (string, error)
}

var newPlaceholderResolver = func() placeholderResolver {
	return auth.NewPlaceholderResolver()
}

type apiKeyInjectionResult struct {
	ProviderHasEnvCredential map[string]bool
	ResolvedSecretIDs        map[string]bool
	SecretValues             map[string]string
	InjectedKeys             map[string]string
	SecretMapping            SecretMapping
}

func newAuthManager(r *Runtime) authManager {
	return authManager{Runtime: r}
}

func (m authManager) Prepare(env *[]string, stores storeSet) (model.AuthState, SecretMapping, error) {
	hooks := auth.HooksFor(m.profile)
	authStorage := stores.AuthStorage()
	authCtx := auth.Context{
		Host:        m.host,
		Project:     m.project,
		Profile:     m.profile,
		Run:         m.run,
		Auth:        m.auth,
		Build:       m.build,
		Storage:     m.storage(),
		ConfigStore: stores.Config,
		AuthStorage: authStorage,
	}
	persistedEnv := m.readPersistedEnv(stores)

	providerSessions := m.checkProviderSessions(authStorage)
	authCtx.StorageHasSession = anyProviderSession(providerSessions)
	if err := m.withStoreLock(stores.Config, func() error {
		_, err := hooks.OnAuthReady(authCtx)
		return err
	}); err != nil {
		logx.Warnf("Auth ready hook failed: %v", err)
	}

	layeredSecrets, err := auth.ResolveLayeredSecrets(m.host.Home, m.project.Hash, m.profile.Name, m.auth.SecretsScope)
	if err != nil {
		logx.Warnf("Failed to read layered secrets: %v", err)
		layeredSecrets = map[string]string{}
	}

	activeSecrets, err := m.activeSecrets()
	if err != nil {
		return model.AuthState{}, SecretMapping{}, fmt.Errorf("resolve active secrets: %w", err)
	}
	injection, err := m.injectDeclaredSecrets(hooks, authCtx, env, persistedEnv, layeredSecrets, authStorage, activeSecrets)
	if err != nil {
		return model.AuthState{}, SecretMapping{}, err
	}
	passEnvValues := m.injectPassEnv(env, persistedEnv, injection.InjectedKeys)
	m.persistEnvToStore(stores, persistedEnv, injection.SecretValues, passEnvValues)

	authState := model.AuthState{
		Providers: map[string]model.ProviderAuthState{},
	}
	for _, provider := range m.profile.Providers {
		name := provider.Name
		if name == "" {
			continue
		}
		authState.Providers[name] = model.ProviderAuthState{
			HasEnvCredential: injection.ProviderHasEnvCredential[name],
			HasSession:       providerSessions[name],
		}
	}
	if err := m.withStoreLock(stores.Config, func() error {
		return hooks.FinalizeAuth(authCtx, authState)
	}); err != nil {
		logx.Warnf("Finalize auth hook failed: %v", err)
	}

	return authState, injection.SecretMapping, nil
}

func (m authManager) readPersistedEnv(stores storeSet) map[string]string {
	persistedEnv := map[string]string{}
	if stores.PersistedEnvAvailable && stores.Env != nil {
		if err := m.withStoreLock(stores.Env, func() error {
			if env, ok := m.readEnvStore(); ok {
				persistedEnv = env
			}
			return nil
		}); err != nil {
			logx.Warnf("Failed to read persisted env store: %v", err)
		}
	}
	return persistedEnv
}

func (m authManager) checkProviderSessions(authStorage *backend.StoreRef) map[string]bool {
	sessions := map[string]bool{}
	for _, provider := range m.profile.Providers {
		if provider.Name == "" {
			continue
		}
		sessions[provider.Name] = m.checkProviderSession(authStorage, provider)
	}
	return sessions
}

func (m authManager) checkProviderSession(authStorage *backend.StoreRef, provider model.ProviderConfig) bool {
	if authStorage == nil {
		return false
	}
	if provider.AuthSession != nil && len(provider.AuthSession.Checks) > 0 {
		session, err := m.checkSessionFromConfig(authStorage, *provider.AuthSession)
		if err == nil {
			return session
		}
		logx.Warnf("Failed to evaluate auth session checks for %s/%s: %v", m.profile.Name, provider.Name, err)
		return false
	}
	if len(provider.AuthFiles) == 0 {
		return false
	}
	return m.checkSessionFromAuthFiles(authStorage, provider.AuthFiles)
}

func (m authManager) injectDeclaredSecrets(hooks auth.Hooks, authCtx auth.Context, env *[]string, persistedEnv map[string]string, layeredSecrets map[string]string, authStorage *backend.StoreRef, activeSecrets []activeSecret) (apiKeyInjectionResult, error) {
	result := apiKeyInjectionResult{
		ProviderHasEnvCredential: map[string]bool{},
		ResolvedSecretIDs:        map[string]bool{},
		SecretValues:             map[string]string{},
		InjectedKeys:             map[string]string{},
		SecretMapping:            SecretMapping{},
	}
	injectedAny := false
	placeholderResolver := newPlaceholderResolver()
	hookInjectedKeys := map[string]string{}
	suppressedAPIKeySecrets, suppressionReason := m.suppressedAPIKeySecrets()
	eligibleSecrets, suppressedActiveSecrets := partitionInjectableSecrets(activeSecrets, suppressedAPIKeySecrets)
	for _, secret := range suppressedActiveSecrets {
		logx.Debugf("Suppressed declared API key secret %s due to %s", secret.ID, suppressionReason)
	}
	secretReleaseEnabled := m.shouldUseSecretReleases(eligibleSecrets)
	for _, secret := range eligibleSecrets {
		secretValue, secretSource, found, err := resolveActiveSecretValue(secret, m.host.Home, layeredSecrets, persistedEnv)
		if err != nil {
			return result, fmt.Errorf("resolve secret %q: %w", secret.ID, err)
		}
		if !found {
			continue
		}
		placeholder := ""
		var placeholderVars map[string]bool
		if secretReleaseEnabled && secret.ReleaseHTTP != nil {
			resolved, err := placeholderResolver.ResolvePlaceholder(secret.ID)
			if err != nil {
				logx.Warnf("Secret release disabled for %s due to placeholder error: %v", secret.ID, err)
			} else {
				placeholder = resolved
				placeholderVars = m.proxyManagedEnvVars(secret)
				result.SecretMapping.Entries = append(result.SecretMapping.Entries, model.SecretReleaseEntry{
					SecretID:    secret.ID,
					Placeholder: placeholder,
					Value:       secretValue,
					Hosts:       append([]string{}, secret.ReleaseHTTP.Hosts...),
					Header:      secret.ReleaseHTTP.Header,
					Format:      secret.ReleaseHTTP.Format,
				})
			}
		}
		for _, envVar := range secret.EnvVars {
			envValue := secretValue
			if placeholder != "" && placeholderVars[envVar] {
				envValue = placeholder
			}
			result.SecretValues[envVar] = secretValue
			result.InjectedKeys[envVar] = envValue
			hookInjectedKeys[envVar] = secretValue
			*env = append(*env, envVar+"="+envValue)
			logx.Debugf("Injected %s from %s (%s)", envVar, secretSource, util.RedactSecret(envValue))
		}
		result.ResolvedSecretIDs[secret.ID] = true
		injectedAny = true
	}
	if injectedAny {
		if err := m.withStoreLock(authStorage, func() error {
			return hooks.AfterEnvInjected(authCtx, hookInjectedKeys)
		}); err != nil {
			logx.Warnf("Post-injection auth hook failed: %v", err)
		}
	} else if len(eligibleSecrets) > 0 {
		logx.Warnf("No declared secret found for %s.", m.profile.Name)
	}
	if injectedAny {
		for _, provider := range m.profile.Providers {
			for _, secretID := range provider.CredentialSecrets {
				if result.ResolvedSecretIDs[secretID] {
					result.ProviderHasEnvCredential[provider.Name] = true
					break
				}
			}
		}
	}
	return result, nil
}

// proxyManagedEnvVars returns the set of the secret's env-var aliases that
// should be injected as secret-release placeholders. environment.proxyManaged
// (from the tool spec and all enabled mixins) names the vars the network proxy
// swaps for the real secret at request time. When a release-eligible secret
// has any of its aliases listed there, only those aliases carry the
// placeholder and the remaining aliases receive the raw value. When
// proxyManaged is unset — or names none of this secret's aliases — every
// alias is placeholder-managed, preserving the behavior from before
// proxyManaged was honored.
func (m authManager) proxyManagedEnvVars(secret activeSecret) map[string]bool {
	all := make(map[string]bool, len(secret.EnvVars))
	for _, envVar := range secret.EnvVars {
		all[envVar] = true
	}
	managed := map[string]bool{}
	for _, envVar := range m.specProxyManaged() {
		if all[envVar] {
			managed[envVar] = true
		}
	}
	if len(managed) == 0 {
		return all
	}
	return managed
}

func partitionInjectableSecrets(activeSecrets []activeSecret, suppressedSecrets map[string]bool) (eligible []activeSecret, suppressed []activeSecret) {
	if len(suppressedSecrets) == 0 {
		return activeSecrets, nil
	}
	eligible = make([]activeSecret, 0, len(activeSecrets))
	suppressed = make([]activeSecret, 0, len(activeSecrets))
	for _, secret := range activeSecrets {
		if suppressedSecrets[secret.ID] {
			suppressed = append(suppressed, secret)
			continue
		}
		eligible = append(eligible, secret)
	}
	return eligible, suppressed
}

func (m authManager) injectPassEnv(env *[]string, persistedEnv map[string]string, injectedKeys map[string]string) map[string]string {
	if len(m.auth.PassEnv) == 0 {
		return nil
	}
	passEnvValues := map[string]string{}
	for _, keyVar := range m.auth.PassEnv {
		if _, ok := injectedKeys[keyVar]; ok {
			continue
		}
		keyValue := os.Getenv(keyVar)
		keySource := ""
		if keyValue != "" {
			keySource = "env"
		} else if value, ok := persistedEnv[keyVar]; ok {
			keyValue = value
			keySource = "persisted"
		}
		if keyValue == "" {
			logx.Warnf("No value found for passed env key %s.", keyVar)
			continue
		}
		passEnvValues[keyVar] = keyValue
		injectedKeys[keyVar] = keyValue
		*env = append(*env, keyVar+"="+keyValue)
		logx.Debugf("Injected %s from %s (%s)", keyVar, keySource, util.RedactSecret(keyValue))
	}
	return passEnvValues
}

func (m authManager) persistEnvToStore(stores storeSet, persistedEnv map[string]string, secretValues map[string]string, passEnvValues map[string]string) {
	if !m.run.Persist || stores.Env == nil {
		return
	}
	if len(secretValues) == 0 && len(passEnvValues) == 0 {
		return
	}
	if err := m.withStoreLock(stores.Env, func() error {
		volumeEnv, ok := m.readEnvStore()
		baseEnv := persistedEnv
		if ok {
			baseEnv = volumeEnv
		}
		envToPersist := mergeEnvForPersist(baseEnv, secretValues, passEnvValues)
		if envContent := formatEnvContent(envToPersist); envContent != "" {
			return m.writeEnvStore(envContent)
		}
		return nil
	}); err != nil {
		logx.Warnf("Failed to persist env store: %v", err)
	}
}

// envStoreKey identifies the persisted-env store for this tool + project.
func (m authManager) envStoreKey() backend.StoreKey {
	return envStoreKey(m.profile.Name, m.project.Hash)
}

func (m authManager) storage() backend.StoreManager {
	if m.backend == nil {
		return nil
	}
	return m.backend.Storage()
}

// readEnvStore reads and parses the persisted "env" file from the env store.
func (m authManager) readEnvStore() (map[string]string, bool) {
	store := m.storage()
	if store == nil {
		return nil, false
	}
	data, err := store.ReadFile(context.Background(), m.envStoreKey(), backend.StoreKindEnv, "env")
	if err != nil {
		return nil, false
	}
	env, err := util.ParseEnv(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	return env, true
}

// writeEnvStore writes the "env" file and restores agent ownership of the store
// (matching the previous WriteEnvToVolume chown), so the in-container user can
// read it back.
func (m authManager) writeEnvStore(envContent string) error {
	store := m.storage()
	if store == nil {
		return fmt.Errorf("backend storage unavailable")
	}
	key := m.envStoreKey()
	if owner := configSourceChownSpec(m.host.UID, m.host.GID); owner != "" {
		if writer, ok := store.(backend.OwnedFileWriter); ok {
			return writer.WriteFileOwned(context.Background(), key, backend.StoreKindEnv, "env", []byte(envContent), 0o600, owner)
		}
		if err := store.WriteFile(context.Background(), key, backend.StoreKindEnv, "env", []byte(envContent), 0o600); err != nil {
			return err
		}
		return store.Ensure(context.Background(), key, backend.StoreKindEnv, owner)
	}
	return store.WriteFile(context.Background(), key, backend.StoreKindEnv, "env", []byte(envContent), 0o600)
}

// mergeEnvForPersist merges env value maps in priority order. Later arguments
// override earlier ones: existing → declared secrets → passEnv.
func mergeEnvForPersist(existing map[string]string, secretValues map[string]string, passEnvValues map[string]string) map[string]string {
	result := map[string]string{}
	for k, v := range existing {
		result[k] = v
	}
	for k, v := range secretValues {
		if v != "" {
			result[k] = v
		}
	}
	for k, v := range passEnvValues {
		if v != "" {
			result[k] = v
		}
	}
	return result
}

func (m authManager) suppressedAPIKeySecrets() (map[string]bool, string) {
	reason := m.apiKeySecretSuppressionReason()
	if reason == "" {
		return nil, ""
	}
	return m.profile.ProviderAPIKeySecretIDs(), reason
}

func (m authManager) apiKeySecretSuppressionReason() string {
	if m.auth.NoAPIKey {
		return "--no-api-key"
	}
	if m.run.Ephemeral && !m.auth.PassAPIKey {
		return "--ephemeral without --pass-api-key"
	}
	return ""
}

func (m authManager) shouldUseSecretReleases(activeSecrets []activeSecret) bool {
	hasHTTPRelease := false
	for _, secret := range activeSecrets {
		if secret.ReleaseHTTP != nil {
			hasHTTPRelease = true
			break
		}
	}
	if !hasHTTPRelease {
		return false
	}
	resolved, err := m.resolveEffectivePolicy()
	if err != nil {
		logx.Warnf("Failed to resolve effective network policy for secret release decision: %v", err)
		return true
	}
	return resolved.Effective.Mode != model.NetworkModeUnrestricted
}

func anyProviderSession(sessions map[string]bool) bool {
	for _, hasSession := range sessions {
		if hasSession {
			return true
		}
	}
	return false
}
