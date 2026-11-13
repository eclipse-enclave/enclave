// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package auth

import (
	"fmt"
	"os"

	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/secretfile"
)

// SecretSources are the host sources a declared credential resolves from:
// the host environment, the layered secrets files, the persisted env store,
// and the credential's own file source. Nothing in it is readable by a
// container.
type SecretSources struct {
	// Home expands "~" in file sources.
	Home string
	// Layers are the secrets files, lowest precedence first, as
	// ResolveSecretsLayers returns them.
	Layers []SecretsLayer
	// Persisted is the env store.
	Persisted map[string]string
	// Getenv reads the host environment. Nil reads os.Getenv.
	Getenv func(string) string
}

// SecretRef is a declared credential: env aliases, an optional file source,
// and the order of the two.
type SecretRef struct {
	// ID names the credential in errors and warnings.
	ID       string
	EnvVars  []string
	File     *model.SecretFileSource
	Priority string
}

// Resolve returns the credential's value, where it came from ("env",
// "secrets", "persisted", or "file"), and whether it was found. The priority
// orders the env aliases against the file source: file-first consults the
// file before the env aliases, env-first (the default) the reverse. A
// malformed file parser fails regardless of order.
func (s SecretSources) Resolve(ref SecretRef) (string, string, bool, error) {
	if ref.Priority == model.SecretPriorityFileFirst {
		value, source, found, err := s.resolveFile(ref)
		if err != nil || found {
			return value, source, found, err
		}
		return s.ResolveAliases(ref)
	}
	value, source, found, err := s.ResolveAliases(ref)
	if err != nil || found {
		return value, source, found, err
	}
	return s.resolveFile(ref)
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

// ResolveAliases resolves a credential through its env aliases, walking the
// host environment, then the layered secrets files (highest layer first),
// then the persisted env store. The highest layer wins, and only aliases
// resolved from the same layer must agree. Drift between layers is logged.
func (s SecretSources) ResolveAliases(ref SecretRef) (string, string, bool, error) {
	values := make([]aliasValue, 0, len(ref.EnvVars))
	for _, envVar := range ref.EnvVars {
		if resolved, ok := s.resolveAlias(envVar); ok {
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
			return "", "", false, fmt.Errorf("secret %q has conflicting values across env aliases (%s vs %s, both set in %s)", ref.ID, chosen.envVar, value.envVar, chosen.layer)
		}
		logx.Warnf("Secret %s: using %s from %s, %s in %s holds a different value and is ignored.", ref.ID, chosen.envVar, chosen.layer, value.envVar, value.layer)
	}
	return chosen.value, chosen.source, true, nil
}

func (s SecretSources) resolveAlias(envVar string) (aliasValue, bool) {
	getenv := s.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if value := getenv(envVar); value != "" {
		return aliasValue{envVar: envVar, value: value, source: "env", layer: "the host environment", rank: 0}, true
	}
	for i := len(s.Layers) - 1; i >= 0; i-- {
		if value := s.Layers[i].Values[envVar]; value != "" {
			return aliasValue{envVar: envVar, value: value, source: "secrets", layer: s.Layers[i].Path, rank: len(s.Layers) - i}, true
		}
	}
	if value := s.Persisted[envVar]; value != "" {
		return aliasValue{envVar: envVar, value: value, source: "persisted", layer: "the host-side env store", rank: len(s.Layers) + 1}, true
	}
	return aliasValue{}, false
}

// resolveFile reads the file source, if any. A missing file or an empty value
// reports found=false so the caller can fall back to the env aliases. A
// malformed parser or file content fails.
func (s SecretSources) resolveFile(ref SecretRef) (string, string, bool, error) {
	if ref.File == nil || ref.File.Path == "" {
		return "", "", false, nil
	}
	value, found, err := secretfile.ResolveFileSecret(s.Home, ref.File.Path, ref.File.Parser)
	if err != nil {
		return "", "", false, fmt.Errorf("secret %q file source: %w", ref.ID, err)
	}
	if !found || value == "" {
		return "", "", false, nil
	}
	return value, "file", true, nil
}
