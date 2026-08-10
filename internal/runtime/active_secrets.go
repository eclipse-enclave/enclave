// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"

	"enclave/internal/domainpattern"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/secretfile"
	"enclave/internal/util"
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

func resolveActiveSecretValue(secret activeSecret, hostHome string, layeredSecrets map[string]string, persistedEnv map[string]string) (string, string, bool, error) {
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
		return resolveEnvAliasValue(secret, layeredSecrets, persistedEnv)
	}

	value, source, found, err := resolveEnvAliasValue(secret, layeredSecrets, persistedEnv)
	if err != nil {
		return "", "", false, err
	}
	if found {
		return value, source, true, nil
	}
	return resolveFileSecretValue(secret, hostHome)
}

// resolveEnvAliasValue resolves a secret through its env-var aliases, walking
// host env, then the layered .env secrets, then persisted env.
func resolveEnvAliasValue(secret activeSecret, layeredSecrets map[string]string, persistedEnv map[string]string) (string, string, bool, error) {
	type aliasValue struct {
		envVar   string
		value    string
		source   string
		priority int
		found    bool
	}

	values := make([]aliasValue, 0, len(secret.EnvVars))
	for _, envVar := range secret.EnvVars {
		if value := os.Getenv(envVar); value != "" {
			values = append(values, aliasValue{envVar: envVar, value: value, source: "env", priority: 1, found: true})
			continue
		}
		if value, ok := layeredSecrets[envVar]; ok && value != "" {
			values = append(values, aliasValue{envVar: envVar, value: value, source: "secrets", priority: 2, found: true})
			continue
		}
		if value, ok := persistedEnv[envVar]; ok && value != "" {
			values = append(values, aliasValue{envVar: envVar, value: value, source: "persisted", priority: 3, found: true})
		}
	}

	if len(values) == 0 {
		return "", "", false, nil
	}

	chosen := values[0]
	for _, value := range values[1:] {
		if value.value != chosen.value {
			return "", "", false, fmt.Errorf("secret %q has conflicting values across env aliases (%s vs %s)", secret.ID, chosen.envVar, value.envVar)
		}
		if value.priority < chosen.priority {
			chosen = value
		}
	}

	return chosen.value, chosen.source, true, nil
}

// hostCredentialRefs maps a host-selector credential id to the secret ids whose
// release hosts it overrides. Several services commonly share one host
// credential (gitlab's three tokens), so the reference is resolved once.
func hostCredentialRefs(secrets []activeSecret) map[string][]string {
	refs := map[string][]string{}
	for _, secret := range secrets {
		if secret.ReleaseHTTP != nil && secret.ReleaseHTTP.HostsFromSecret != "" {
			ref := secret.ReleaseHTTP.HostsFromSecret
			refs[ref] = append(refs[ref], secret.ID)
		}
	}
	return refs
}

// resolveReleaseHostOverrides resolves the credentials named by each release
// rule's HostsFromSecret and returns secret-id -> replacement release hosts. A
// service whose host credential is unset or unusable keeps its declared hosts,
// so the default (e.g. gitlab.com) still applies.
//
// The replacement is deliberately not additive: the referenced credential names
// the one instance the token belongs to, and injecting an instance-specific
// token into the default hosts as well would expose it to a service that cannot
// accept it. It narrows token release only — the declared hosts stay in the
// network allow set, see Runtime.releaseHosts.
func resolveReleaseHostOverrides(secrets []activeSecret, hostHome string, layeredSecrets map[string]string, persistedEnv map[string]string) map[string][]string {
	byID := make(map[string]activeSecret, len(secrets))
	for _, secret := range secrets {
		byID[secret.ID] = secret
	}

	overrides := map[string][]string{}
	refs := hostCredentialRefs(secrets)
	for _, ref := range slices.Sorted(maps.Keys(refs)) {
		secretIDs := refs[ref]
		source, ok := byID[ref]
		if !ok {
			continue
		}
		value, _, found, err := resolveActiveSecretValue(source, hostHome, layeredSecrets, persistedEnv)
		if err != nil {
			logx.Warnf("Cannot resolve host credential %s (%v); keeping declared hosts.", ref, err)
			continue
		}
		if !found {
			continue
		}
		host := normalizeReleaseHost(ref, value)
		if host == "" {
			continue
		}
		logx.Infof("Host credential %s selects %s for %s", ref, host, strings.Join(secretIDs, ", "))
		for _, id := range secretIDs {
			overrides[id] = []string{host}
		}
	}
	return overrides
}

// normalizeReleaseHost turns a credential value into an allowlist-comparable
// host, tolerating the URL forms CLIs accept (glab reads GITLAB_HOST as either
// a bare host or a full URL). An unusable value warns and is dropped rather
// than failing the session, since the declared hosts remain valid. The value is
// redacted in the warning: the credential is declared like any other, so a
// misconfigured spec could point this at a token.
func normalizeReleaseHost(secretID string, value string) string {
	trimmed := value
	if index := strings.Index(trimmed, "://"); index >= 0 {
		trimmed = trimmed[index+len("://"):]
	}
	if index := strings.IndexAny(trimmed, "/?#"); index >= 0 {
		trimmed = trimmed[:index]
	}
	// A wildcard would release the token to every host under a suffix, which
	// defeats naming the one instance it belongs to. Normalize accepts them, so
	// reject before it runs.
	if strings.Contains(trimmed, "*") {
		logx.Warnf("Secret %s: value %s is a wildcard pattern, not a host; keeping declared hosts.", secretID, util.RedactSecret(value))
		return ""
	}
	// NormalizeHost strips the port and IPv6 brackets but does not validate the
	// labels, so the pattern validator runs after it to reject junk that would
	// otherwise become a bogus allowlist entry.
	host, err := domainpattern.NormalizeHost(trimmed)
	if err == nil {
		host, err = domainpattern.Normalize(host)
	}
	if err != nil {
		logx.Warnf("Secret %s: value %s is not a usable host (%v); keeping declared hosts.", secretID, util.RedactSecret(value), err)
		return ""
	}
	return host
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
