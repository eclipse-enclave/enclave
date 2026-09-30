// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

func TestRequiredFeatureSecretsInjectOnce(t *testing.T) {
	paths := model.Paths{ToolsDir: "../../extensions/tools", FeaturesDir: "../../extensions/features"}
	for _, tool := range []string{"claude", "theia", "theia-next"} {
		profile, err := config.LoadProfile(paths, tool)
		if err != nil {
			t.Fatal(err)
		}
		features, err := config.ResolveRequiredFeatures(paths, profile.RequiredFeatures)
		if err != nil {
			t.Fatal(err)
		}
		runtime := &Runtime{profile: profile, features: features}
		secrets, err := runtime.activeSecrets()
		if err != nil {
			t.Fatal(err)
		}
		if len(secrets) != 2 {
			t.Fatalf("%s active secrets: %v", tool, secrets)
		}
		eligible, suppressed := partitionInjectableSecrets(secrets, profile.ProviderAPIKeySecretIDs())
		if len(eligible) != 1 || eligible[0].ID != "claude-code-oauth-token" || len(suppressed) != 1 || suppressed[0].ID != "anthropic-api-key" {
			t.Fatalf("OAuth handling changed: eligible %v suppressed %v", eligible, suppressed)
		}
	}
}
