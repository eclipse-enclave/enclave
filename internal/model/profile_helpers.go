// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import (
	"path/filepath"
	"sort"
	"strings"
)

func (p Profile) DeclaredSecretEnvVars() []string {
	return collectSecretEnvVars(p.Secrets)
}

func (e Extension) DeclaredSecretEnvVars() []string {
	return collectSecretEnvVars(e.Secrets)
}

func collectSecretEnvVars(secrets map[string]SecretConfig) []string {
	var vars []string
	ids := make([]string, 0, len(secrets))
	for id := range secrets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		secret := secrets[id]
		for _, key := range secret.EnvVars {
			if key != "" {
				vars = append(vars, key)
			}
		}
	}
	return dedupeStrings(vars)
}

func (c SecretConfig) ReleaseHTTP() *HTTPSecretReleaseConfig {
	if c.Release == nil {
		return nil
	}
	return c.Release.HTTP
}

func (c SecretConfig) IsAPIKey() bool {
	return c.APIKey == nil || *c.APIKey
}

func (p Profile) ProviderAPIKeySecretIDs() map[string]bool {
	secretIDs := map[string]bool{}
	for _, provider := range p.Providers {
		for _, secretID := range provider.CredentialSecrets {
			if secretID == "" {
				continue
			}
			if secret, ok := p.Secrets[secretID]; ok && !secret.IsAPIKey() {
				continue
			}
			secretIDs[secretID] = true
		}
	}
	return secretIDs
}

// ProviderSecurestorageDirEnv returns the first provider-declared
// securestorage_dir_env, or "" when none is set. When non-empty, enclave sets
// this environment variable to the shared auth directory so the tool stores its
// credential file there natively (see ProviderConfig.SecurestorageDirEnv).
func (p Profile) ProviderSecurestorageDirEnv() string {
	for _, provider := range p.Providers {
		if env := strings.TrimSpace(provider.SecurestorageDirEnv); env != "" {
			return env
		}
	}
	return ""
}

func (p Profile) ProviderAuthFiles() []string {
	var files []string
	for _, provider := range p.Providers {
		for _, file := range provider.AuthFiles {
			if file != "" {
				files = append(files, file)
			}
		}
	}
	return dedupeStrings(files)
}

func (p Profile) RuntimeAuthFiles() []string {
	var files []string
	for _, file := range p.ProviderAuthFiles() {
		file = strings.TrimSpace(file)
		if file != "" {
			files = append(files, file)
		}
	}
	if extra := strings.TrimSpace(p.HostOAuthJSON); extra != "" && !filepath.IsAbs(extra) {
		files = append(files, extra)
	}
	return dedupeStrings(files)
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
