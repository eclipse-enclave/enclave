// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package auth

import (
	"fmt"

	"enclave/internal/model"
	"enclave/internal/secretfile"
)

// SecretLookup is what resolving a declared secret needs from its
// declaration.
type SecretLookup struct {
	ID       string
	EnvVars  []string
	File     *model.SecretFileSource
	Priority string
}

// Sources of a resolved secret.
const (
	SecretSourceEnv       = "env"
	SecretSourceSecrets   = "secrets"
	SecretSourcePersisted = "persisted"
	SecretSourceFile      = "file"
)

// ResolvedSecret is the value a session injects for a secret and where it
// came from. EnvVar is the alias that won, empty for a file source. Layer is
// the secrets file path for SecretSourceSecrets, the file path for
// SecretSourceFile, and a description otherwise.
type ResolvedSecret struct {
	Value  string
	EnvVar string
	Source string
	Layer  string
	// Drift describes aliases in lower layers that hold a different value and
	// were ignored. Sessions log it; the value still resolves.
	Drift []string
}

// ResolveSecret resolves a declared secret the way sessions do. Priority
// orders the env aliases against the file source: file-first consults the
// file before the env aliases; env-first (the default) does the reverse. A
// malformed file parser fails loudly regardless of order. getenv reads the
// host environment.
func ResolveSecret(lookup SecretLookup, hostHome string, layers []SecretsLayer, persisted map[string]string, getenv func(string) string) (ResolvedSecret, bool, error) {
	if lookup.Priority == model.SecretPriorityFileFirst {
		if resolved, found, err := resolveFileSource(lookup, hostHome); err != nil || found {
			return resolved, found, err
		}
		return resolveEnvAliases(lookup, layers, persisted, getenv)
	}
	if resolved, found, err := resolveEnvAliases(lookup, layers, persisted, getenv); err != nil || found {
		return resolved, found, err
	}
	return resolveFileSource(lookup, hostHome)
}

// aliasValue is one env alias resolved from one layer. rank orders the layers,
// lowest first: host env, then the secrets files from the highest layer down,
// then the persisted env store.
type aliasValue struct {
	ResolvedSecret
	rank int
}

// resolveEnvAliases walks the host environment, then the layered secrets
// files (highest layer first), then the persisted env store. The highest
// layer wins; only aliases resolved from the same layer must agree. Drift
// between layers is reported in Drift and heals through the persisted
// write-back.
func resolveEnvAliases(lookup SecretLookup, layers []SecretsLayer, persisted map[string]string, getenv func(string) string) (ResolvedSecret, bool, error) {
	values := make([]aliasValue, 0, len(lookup.EnvVars))
	for _, envVar := range lookup.EnvVars {
		if resolved, ok := resolveAlias(envVar, layers, persisted, getenv); ok {
			values = append(values, resolved)
		}
	}
	if len(values) == 0 {
		return ResolvedSecret{}, false, nil
	}

	chosen := values[0]
	for _, value := range values[1:] {
		if value.rank < chosen.rank {
			chosen = value
		}
	}
	resolved := chosen.ResolvedSecret
	for _, value := range values {
		if value.Value == chosen.Value {
			continue
		}
		if value.rank == chosen.rank {
			return ResolvedSecret{}, false, fmt.Errorf("secret %q has conflicting values across env aliases (%s vs %s, both set in %s)", lookup.ID, chosen.EnvVar, value.EnvVar, chosen.Layer)
		}
		resolved.Drift = append(resolved.Drift, fmt.Sprintf("Secret %s: using %s from %s; %s in %s holds a different value and is ignored.", lookup.ID, chosen.EnvVar, chosen.Layer, value.EnvVar, value.Layer))
	}
	return resolved, true, nil
}

func resolveAlias(envVar string, layers []SecretsLayer, persisted map[string]string, getenv func(string) string) (aliasValue, bool) {
	if value := getenv(envVar); value != "" {
		return aliasValue{ResolvedSecret{Value: value, EnvVar: envVar, Source: SecretSourceEnv, Layer: "the host environment"}, 0}, true
	}
	for i := len(layers) - 1; i >= 0; i-- {
		if value := layers[i].Values[envVar]; value != "" {
			return aliasValue{ResolvedSecret{Value: value, EnvVar: envVar, Source: SecretSourceSecrets, Layer: layers[i].Path}, len(layers) - i}, true
		}
	}
	if value := persisted[envVar]; value != "" {
		return aliasValue{ResolvedSecret{Value: value, EnvVar: envVar, Source: SecretSourcePersisted, Layer: "the host-side env store"}, len(layers) + 1}, true
	}
	return aliasValue{}, false
}

// resolveFileSource reads the secret's file source, if any. A missing file
// (or an empty resolved value) reports found=false so the caller can fall back
// to the env aliases; a malformed parser or file content fails loudly.
func resolveFileSource(lookup SecretLookup, hostHome string) (ResolvedSecret, bool, error) {
	if lookup.File == nil || lookup.File.Path == "" {
		return ResolvedSecret{}, false, nil
	}
	value, found, err := secretfile.ResolveFileSecret(hostHome, lookup.File.Path, lookup.File.Parser)
	if err != nil {
		return ResolvedSecret{}, false, fmt.Errorf("secret %q file source: %w", lookup.ID, err)
	}
	if !found || value == "" {
		return ResolvedSecret{}, false, nil
	}
	return ResolvedSecret{Value: value, Source: SecretSourceFile, Layer: lookup.File.Path}, true, nil
}
