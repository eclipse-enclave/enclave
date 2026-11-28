// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package apikeys groups the API keys tools declare by the service they
// unlock, so a key such as "anthropic" can be managed once for every tool
// that uses it.
package apikeys

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"enclave/internal/model"
)

// Use is one tool's declaration of an API key.
type Use struct {
	Tool     string
	Provider string
	SecretID string
	EnvVars  []string
	// RequiredPrefix is the prefix gateway protection demands of the value;
	// sessions with restricted networking refuse any other value.
	RequiredPrefix string
	// File and Priority are the declaration's file source, which session
	// resolution consults alongside the env aliases.
	File     *model.SecretFileSource
	Priority string
}

// Key is an API key and every tool that uses it.
type Key struct {
	// Name is the credential id without its "-api-key" or "-token" suffix
	// ("anthropic" for anthropic-api-key).
	Name string
	// Variable is the alias declared by the most tools; shared secrets files
	// store the key under it.
	Variable string
	// EnvVars is every alias any tool declares, in first-seen order.
	EnvVars []string
	Uses    []Use
}

// FromProfile lists the API keys a tool's providers accept, in provider
// order. OAuth and session tokens (apiKey: false) are left out: they come
// from a login, not from a key the user pastes.
func FromProfile(profile model.Profile) []Use {
	var uses []Use
	seen := map[string]bool{}
	for _, provider := range profile.Providers {
		for _, id := range provider.CredentialSecrets {
			secret, ok := profile.Secrets[id]
			if !ok || seen[id] || !secret.IsAPIKey() || len(secret.EnvVars) == 0 {
				continue
			}
			seen[id] = true
			use := Use{Tool: profile.Name, Provider: provider.Name, SecretID: id, EnvVars: secret.EnvVars, File: secret.File, Priority: secret.Priority}
			if release := secret.ReleaseHTTP(); release != nil {
				use.RequiredPrefix = release.RequiredValuePrefix
			}
			uses = append(uses, use)
		}
	}
	return uses
}

// keyName derives a key's name from its credential id. Grouping uses the whole
// id, not a vendor prefix, so distinct services of one vendor
// (azure-openai-api-key, azure-speech-api-key) stay separate keys.
func keyName(secretID string) string {
	if name, ok := strings.CutSuffix(secretID, "-api-key"); ok {
		return name
	}
	return strings.TrimSuffix(secretID, "-token")
}

// Catalog groups the API keys of profiles by name, sorted by name.
func Catalog(profiles []model.Profile) []Key {
	byName := map[string]*Key{}
	for _, profile := range profiles {
		for _, use := range FromProfile(profile) {
			name := keyName(use.SecretID)
			key := byName[name]
			if key == nil {
				key = &Key{Name: name}
				byName[name] = key
			}
			key.Uses = append(key.Uses, use)
			for _, envVar := range use.EnvVars {
				if !slices.Contains(key.EnvVars, envVar) {
					key.EnvVars = append(key.EnvVars, envVar)
				}
			}
		}
	}
	keys := make([]Key, 0, len(byName))
	for _, key := range byName {
		key.Variable = mostDeclared(*key)
		keys = append(keys, *key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	return keys
}

// mostDeclared returns the alias the most tools declare; ties go to the
// alias seen first.
func mostDeclared(key Key) string {
	best, bestCount := "", 0
	for _, envVar := range key.EnvVars {
		count := 0
		for _, use := range key.Uses {
			if slices.Contains(use.EnvVars, envVar) {
				count++
			}
		}
		if count > bestCount {
			best, bestCount = envVar, count
		}
	}
	return best
}

// Tools lists the tools that use the key, in catalog order.
func (k Key) Tools() []string {
	return ToolsOf(k.Uses)
}

// ToolsOf lists the tools of uses, in order.
func ToolsOf(uses []Use) []string {
	tools := make([]string, len(uses))
	for i, use := range uses {
		tools[i] = use.Tool
	}
	return tools
}

// matches reports whether name refers to the key through one of its uses: a
// provider name, a credential id, its vendor (the id's first segment, so
// "copilot" finds copilot-github), or an env alias. Find rejects a name that
// matches several keys.
func (k Key) matches(name string) bool {
	if slices.Contains(k.EnvVars, name) {
		return true
	}
	for _, use := range k.Uses {
		vendor, _, _ := strings.Cut(use.SecretID, "-")
		if strings.EqualFold(name, use.Provider) || strings.EqualFold(name, use.SecretID) || strings.EqualFold(name, vendor) {
			return true
		}
	}
	return false
}

// Find resolves a user-supplied key name: an exact key name first, then the
// aliases of matches.
func Find(keys []Key, name string) (Key, error) {
	for _, key := range keys {
		if strings.EqualFold(name, key.Name) {
			return key, nil
		}
	}
	var found []Key
	for _, key := range keys {
		if key.matches(name) {
			found = append(found, key)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return Key{}, fmt.Errorf("unknown API key %q; known keys: %s", name, strings.Join(Names(keys), ", "))
	default:
		return Key{}, fmt.Errorf("%q matches several API keys (%s); use the key name", name, strings.Join(Names(found), ", "))
	}
}

// Names lists the key names.
func Names(keys []Key) []string {
	names := make([]string, len(keys))
	for i, key := range keys {
		names[i] = key.Name
	}
	return names
}
