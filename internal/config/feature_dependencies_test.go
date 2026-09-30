// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"reflect"
	"strings"
	"testing"

	"enclave/internal/model"
)

func TestFeatureDependencies(t *testing.T) {
	features := []model.Extension{
		{Name: "consumer", Priority: 1, RequiredFeatures: []string{"left", "right"}},
		{Name: "left", RequiredFeatures: []string{"core"}},
		{Name: "right", RequiredFeatures: []string{"core"}},
		{Name: "core", Priority: 999},
	}
	got, err := ResolveFeatureDependencies(features, []string{"consumer", "core"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, feature := range got {
		names = append(names, feature.Name)
	}
	if want := []string{"core", "left", "right", "consumer"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("got %v want %v", names, want)
	}
	if _, err := ResolveFeatureDependencies(features, []string{"consumer"}, map[string]bool{"core": true}); err == nil || !strings.Contains(err.Error(), "explicitly excluded") {
		t.Fatalf("exclusion error: %v", err)
	}
	if _, err := ResolveFeatureDependencies(features[:3], []string{"consumer"}, nil); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing dependency error: %v", err)
	}
	features[3].RequiredFeatures = []string{"consumer"}
	if _, err := ResolveFeatureDependencies(features, []string{"consumer"}, nil); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error: %v", err)
	}
}

func TestClaudeCoreConsumers(t *testing.T) {
	paths := model.Paths{ToolsDir: "../../extensions/tools", FeaturesDir: "../../extensions/features"}
	legacyPaths := model.Paths{ToolsDir: "testdata/legacy-spec/tools", FeaturesDir: "testdata/legacy-spec/features"}
	core, err := LoadFeatureExtension(paths, "claude-core")
	if err != nil {
		t.Fatal(err)
	}
	if core.DefaultEnabled || core.ConfigDir != "" || len(core.AuthFiles) != 0 {
		t.Fatalf("core must be opt-in and have no writable auth store: %+v", core)
	}
	for _, name := range []string{"claude", "theia", "theia-next"} {
		t.Run(name, func(t *testing.T) {
			profile, err := LoadProfile(paths, name)
			if err != nil {
				t.Fatal(err)
			}
			legacy, err := LoadProfile(legacyPaths, name)
			if err != nil {
				t.Fatal(err)
			}
			if profile.Command != legacy.Command || profile.ConfigDir != legacy.ConfigDir || profile.MemoryDir != legacy.MemoryDir || !reflect.DeepEqual(profile.Providers, legacy.Providers) {
				t.Fatal("tool launch, stores, or providers changed")
			}
			if !reflect.DeepEqual(profile.RequiredFeatures, []string{"claude-core"}) {
				t.Fatalf("requirements: %v", profile.RequiredFeatures)
			}
			if len(profile.Secrets) != 0 || !reflect.DeepEqual(profile.DependencySecrets, core.Secrets) {
				t.Fatal("credentials must be declared only on core")
			}
			if !reflect.DeepEqual(profile.DeclaredSecretEnvVars(), core.DeclaredSecretEnvVars()) {
				t.Fatal("required credentials missing from devcontainer environment")
			}
			apiKeys := profile.ProviderAPIKeySecretIDs()
			if !apiKeys["anthropic-api-key"] || apiKeys["claude-code-oauth-token"] {
				t.Fatalf("API-key suppression must preserve OAuth token: %v", apiKeys)
			}
			for id, secret := range legacy.Secrets {
				if !reflect.DeepEqual(core.Secrets[id], secret) {
					t.Fatalf("secret %s changed", id)
				}
			}
			if want := []string{"anthropic.com", "claude.ai", "platform.claude.com", "console.anthropic.com", "platform.anthropic.com"}; !reflect.DeepEqual(core.AllowedDomains, want) {
				t.Fatalf("Anthropic allowlist domains: %v", core.AllowedDomains)
			}
		})
	}
}
