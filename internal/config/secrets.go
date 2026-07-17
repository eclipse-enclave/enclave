// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"enclave/internal/domainpattern"
	"enclave/internal/model"
	"enclave/internal/secretfile"
	"enclave/internal/util"
)

var (
	envVarNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	secretIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

func validateAndNormalizeSecretConfigs(secrets map[string]model.SecretConfig) (map[string]model.SecretConfig, error) {
	if len(secrets) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(secrets))
	for id := range secrets {
		keys = append(keys, id)
	}
	sort.Strings(keys)

	normalized := make(map[string]model.SecretConfig, len(secrets))
	seenEnvVars := map[string]string{}
	for _, rawID := range keys {
		cfg := secrets[rawID]
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, fmt.Errorf("secrets contains an empty secret ID")
		}
		if !secretIDPattern.MatchString(id) {
			return nil, fmt.Errorf("secrets[%q]: invalid secret ID", rawID)
		}
		if _, exists := normalized[id]; exists {
			return nil, fmt.Errorf("secrets[%q]: duplicate secret ID after normalization (%q)", rawID, id)
		}

		envVars, err := normalizeSecretEnvVars(rawID, cfg.EnvVars)
		if err != nil {
			return nil, err
		}
		for _, envVar := range envVars {
			if previous, exists := seenEnvVars[envVar]; exists {
				return nil, fmt.Errorf("secrets[%q]: env var %q already declared by %q", rawID, envVar, previous)
			}
			seenEnvVars[envVar] = id
		}

		release, err := normalizeSecretRelease(rawID, cfg.Release)
		if err != nil {
			return nil, err
		}
		file, err := normalizeSecretFileSource(rawID, cfg.File)
		if err != nil {
			return nil, err
		}
		priority, err := normalizeSecretPriority(rawID, cfg.Priority, file != nil)
		if err != nil {
			return nil, err
		}
		normalized[id] = model.SecretConfig{
			EnvVars:  envVars,
			Release:  release,
			APIKey:   cloneBoolPtr(cfg.APIKey),
			File:     file,
			Priority: priority,
		}
	}

	return normalized, nil
}

func validateAndNormalizeProviderCredentialSecrets(providers []model.ProviderConfig, secretIDs map[string]struct{}) error {
	for i := range providers {
		label := fmt.Sprintf("providers[%d]", i)
		if name := strings.TrimSpace(providers[i].Name); name != "" {
			label = fmt.Sprintf("providers[%q]", name)
		}

		normalized := make([]string, 0, len(providers[i].CredentialSecrets))
		seen := map[string]struct{}{}
		for _, rawID := range providers[i].CredentialSecrets {
			id := strings.TrimSpace(rawID)
			if id == "" {
				return fmt.Errorf("%s: credential_secrets contains an empty secret ID", label)
			}
			if _, ok := secretIDs[id]; !ok {
				return fmt.Errorf("%s: credential secret %q is not declared in secrets", label, id)
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			normalized = append(normalized, id)
		}
		providers[i].CredentialSecrets = normalized
	}
	return nil
}

// validateAndNormalizeProviderSecurestorage validates that any declared
// securestorage_dir_env is a well-formed environment variable name. The runtime
// emits it verbatim as a container env key (see runtime.addAuthMount), so an
// invalid name would silently corrupt the container environment.
func validateAndNormalizeProviderSecurestorage(providers []model.ProviderConfig) error {
	for i := range providers {
		env := strings.TrimSpace(providers[i].SecurestorageDirEnv)
		providers[i].SecurestorageDirEnv = env
		if env == "" {
			continue
		}
		if !envVarNamePattern.MatchString(env) {
			label := fmt.Sprintf("providers[%d]", i)
			if name := strings.TrimSpace(providers[i].Name); name != "" {
				label = fmt.Sprintf("providers[%q]", name)
			}
			return fmt.Errorf("%s: invalid securestorage_dir_env %q", label, env)
		}
	}
	return nil
}

func normalizeSecretEnvVars(secretID string, envVars []string) ([]string, error) {
	normalized := make([]string, 0, len(envVars))
	for _, envVar := range envVars {
		name := strings.TrimSpace(envVar)
		if name == "" {
			continue
		}
		if !envVarNamePattern.MatchString(name) {
			return nil, fmt.Errorf("secrets[%q]: invalid env var name %q", secretID, envVar)
		}
		normalized = append(normalized, name)
	}
	normalized = util.Dedupe(normalized)
	if len(normalized) == 0 {
		return nil, fmt.Errorf("secrets[%q]: env_vars must contain at least one env var name", secretID)
	}
	return normalized, nil
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// cloneSecretFileSource deep-copies a *model.SecretFileSource so the normalized
// map does not alias the caller's input pointer (a shared pointer would let a
// later mutation of one leak into the other).
func cloneSecretFileSource(in *model.SecretFileSource) *model.SecretFileSource {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

// normalizeSecretFileSource validates and clones a secret's file source. A
// non-nil source with an empty path is a spec error (the resolver would have
// nothing to read); the parser is validated with the exact rule the runtime
// resolver enforces so a bad parser fails loudly at load rather than silently
// at runtime.
func normalizeSecretFileSource(secretID string, file *model.SecretFileSource) (*model.SecretFileSource, error) {
	if file == nil {
		return nil, nil
	}
	if strings.TrimSpace(file.Path) == "" {
		return nil, fmt.Errorf("secrets[%q]: file source requires a non-empty path", secretID)
	}
	if err := secretfile.ValidateFileParser(file.Parser); err != nil {
		return nil, fmt.Errorf("secrets[%q]: %w", secretID, err)
	}
	return cloneSecretFileSource(file), nil
}

// normalizeSecretPriority validates a secret's credential-source priority. An
// invalid value is always rejected. An empty value is normalized to env-first
// only when the secret has a file source (the sole case where priority is
// meaningful); without a file the empty value is preserved so secrets that
// never opted into a file source serialize unchanged (the runtime already
// treats an empty priority as env-first).
func normalizeSecretPriority(secretID string, priority string, hasFile bool) (string, error) {
	switch strings.TrimSpace(priority) {
	case "":
		if hasFile {
			return model.SecretPriorityEnvFirst, nil
		}
		return "", nil
	case model.SecretPriorityEnvFirst:
		return model.SecretPriorityEnvFirst, nil
	case model.SecretPriorityFileFirst:
		return model.SecretPriorityFileFirst, nil
	default:
		return "", fmt.Errorf("secrets[%q]: invalid priority %q (want %q or %q)", secretID, priority, model.SecretPriorityEnvFirst, model.SecretPriorityFileFirst)
	}
}

func normalizeSecretRelease(secretID string, release *model.SecretReleaseConfig) (*model.SecretReleaseConfig, error) {
	if release == nil {
		return nil, nil
	}
	if release.HTTP == nil {
		return nil, fmt.Errorf("secrets[%q]: release must define http", secretID)
	}

	header := strings.ToLower(strings.TrimSpace(release.HTTP.Header))
	if header == "" {
		return nil, fmt.Errorf("secrets[%q].release.http: header is required", secretID)
	}
	if strings.ContainsAny(header, "\r\n") {
		return nil, fmt.Errorf("secrets[%q].release.http: header must not contain newlines", secretID)
	}

	hosts, err := normalizeHosts(release.HTTP.Hosts)
	if err != nil {
		return nil, fmt.Errorf("secrets[%q].release.http: %w", secretID, err)
	}

	format := strings.TrimSpace(release.HTTP.Format)
	if format != "" && !strings.Contains(format, "%s") {
		return nil, fmt.Errorf("secrets[%q].release.http: format must contain %%s", secretID)
	}

	return &model.SecretReleaseConfig{
		HTTP: &model.HTTPSecretReleaseConfig{
			Hosts:  hosts,
			Header: header,
			Format: format,
		},
	}, nil
}

func normalizeHosts(hosts []string) ([]string, error) {
	normalized := make([]string, 0, len(hosts))
	for _, host := range hosts {
		value, err := domainpattern.Normalize(host)
		if err != nil {
			return nil, fmt.Errorf("invalid host pattern %q: %w", strings.TrimSpace(host), err)
		}
		if value == "" {
			continue
		}
		normalized = append(normalized, value)
	}
	normalized = util.Dedupe(normalized)
	if len(normalized) == 0 {
		return nil, fmt.Errorf("hosts must contain at least one domain pattern")
	}
	return normalized, nil
}
