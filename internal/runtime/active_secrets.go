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
	"sort"

	"enclave/internal/auth"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/secretfile"
)

type activeSecret struct {
	ID          string
	EnvVars     []string
	ReleaseHTTP *model.HTTPSecretReleaseConfig
	File        *model.SecretFileSource
	Priority    string
	source      string
}

func (r *Runtime) activeSecrets() ([]activeSecret, error) {
	type secretSource struct {
		name    string
		secrets map[string]model.SecretConfig
	}

	sources := []secretSource{{
		name:    fmt.Sprintf("tool %q", r.profile.Name),
		secrets: r.profile.Secrets,
	}}
	for _, feature := range r.features {
		if len(feature.Secrets) == 0 {
			continue
		}
		sources = append(sources, secretSource{
			name:    fmt.Sprintf("feature %q", feature.Name),
			secrets: feature.Secrets,
		})
	}

	merged := map[string]activeSecret{}
	seenEnvVars := map[string]string{}
	for _, source := range sources {
		if len(source.secrets) == 0 {
			continue
		}
		ids := make([]string, 0, len(source.secrets))
		for id := range source.secrets {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			cfg := source.secrets[id]
			if existing, ok := merged[id]; ok {
				return nil, fmt.Errorf("secret %q declared by both %s and %s", id, existing.source, source.name)
			}
			for _, envVar := range cfg.EnvVars {
				if existingID, ok := seenEnvVars[envVar]; ok {
					return nil, fmt.Errorf("env var %q declared by both secret %q and %q", envVar, existingID, id)
				}
				seenEnvVars[envVar] = id
			}
			merged[id] = activeSecret{
				ID:          id,
				EnvVars:     append([]string{}, cfg.EnvVars...),
				ReleaseHTTP: cfg.ReleaseHTTP(),
				File:        cfg.File,
				Priority:    cfg.Priority,
				source:      source.name,
			}
		}
	}

	if len(merged) == 0 {
		return nil, nil
	}

	ids := make([]string, 0, len(merged))
	for id := range merged {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	secrets := make([]activeSecret, 0, len(ids))
	for _, id := range ids {
		secrets = append(secrets, merged[id])
	}
	return secrets, nil
}

func resolveActiveSecretValue(secret activeSecret, hostHome string, secretsLayers []auth.SecretsLayer, persistedEnv map[string]string) (string, string, bool, error) {
	// priority orders the env aliases against the file source. file-first
	// consults the file before the env aliases; env-first (the default) does
	// the reverse. A malformed file parser fails loudly regardless of order.
	if secret.Priority == model.SecretPriorityFileFirst {
		value, source, found, err := resolveFileSecretValue(secret, hostHome)
		if err != nil {
			return "", "", false, err
		}
		if found {
			return value, source, true, nil
		}
		return resolveEnvAliasValue(secret, secretsLayers, persistedEnv)
	}

	value, source, found, err := resolveEnvAliasValue(secret, secretsLayers, persistedEnv)
	if err != nil {
		return "", "", false, err
	}
	if found {
		return value, source, true, nil
	}
	return resolveFileSecretValue(secret, hostHome)
}

// aliasValue is one env alias resolved from one layer. rank orders the layers,
// lowest first: host env, then the secrets files from the highest layer down,
// then the persisted env store.
type aliasValue struct {
	envVar string
	value  string
	source string
	layer  string
	rank   int
}

// resolveEnvAliasValue resolves a secret through its env-var aliases, walking
// the host environment, then the layered secrets files (highest layer first),
// then the persisted env store. The highest layer wins; only aliases resolved
// from the same layer must agree. Drift between layers is logged and heals
// through the persisted write-back.
func resolveEnvAliasValue(secret activeSecret, secretsLayers []auth.SecretsLayer, persistedEnv map[string]string) (string, string, bool, error) {
	values := make([]aliasValue, 0, len(secret.EnvVars))
	for _, envVar := range secret.EnvVars {
		if resolved, ok := resolveAlias(envVar, secretsLayers, persistedEnv); ok {
			values = append(values, resolved)
		}
	}

	if len(values) == 0 {
		return "", "", false, nil
	}

	chosen := values[0]
	for _, value := range values[1:] {
		if value.rank < chosen.rank {
			chosen = value
		}
	}

	for _, value := range values {
		if value.value == chosen.value {
			continue
		}
		if value.rank == chosen.rank {
			return "", "", false, fmt.Errorf("secret %q has conflicting values across env aliases (%s vs %s, both set in %s)", secret.ID, chosen.envVar, value.envVar, chosen.layer)
		}
		logx.Warnf("Secret %s: using %s from %s; %s in %s holds a different value and is ignored.", secret.ID, chosen.envVar, chosen.layer, value.envVar, value.layer)
	}

	return chosen.value, chosen.source, true, nil
}

func resolveAlias(envVar string, secretsLayers []auth.SecretsLayer, persistedEnv map[string]string) (aliasValue, bool) {
	if value := os.Getenv(envVar); value != "" {
		return aliasValue{envVar: envVar, value: value, source: "env", layer: "the host environment", rank: 0}, true
	}
	for i := len(secretsLayers) - 1; i >= 0; i-- {
		if value := secretsLayers[i].Values[envVar]; value != "" {
			return aliasValue{envVar: envVar, value: value, source: "secrets", layer: secretsLayers[i].Path, rank: len(secretsLayers) - i}, true
		}
	}
	if value := persistedEnv[envVar]; value != "" {
		return aliasValue{envVar: envVar, value: value, source: "persisted", layer: "the host-side env store", rank: len(secretsLayers) + 1}, true
	}
	return aliasValue{}, false
}

// resolveFileSecretValue reads the secret's file source, if any. A missing file
// (or an empty resolved value) reports found=false so the caller can fall back
// to the env aliases; a malformed parser or file content fails loudly.
func resolveFileSecretValue(secret activeSecret, hostHome string) (string, string, bool, error) {
	if secret.File == nil || secret.File.Path == "" {
		return "", "", false, nil
	}
	value, found, err := secretfile.ResolveFileSecret(hostHome, secret.File.Path, secret.File.Parser)
	if err != nil {
		return "", "", false, fmt.Errorf("secret %q file source: %w", secret.ID, err)
	}
	if !found || value == "" {
		return "", "", false, nil
	}
	return value, "file", true, nil
}
