// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"testing"

	"enclave/internal/model"
)

func TestClaudeAuthSecretsSpecProjection(t *testing.T) {
	paths := realRepoPaths(t)
	doc, err := LoadSpec(paths, "claude", KindSandbox)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := LoadProfile(paths, "claude")
	if err != nil {
		t.Fatal(err)
	}
	provider := profile.Providers[0]
	if len(provider.AuthSecrets) != 3 || !provider.AuthSecrets[2].CheckOnly || len(provider.AuthFiles) != 2 || provider.TokenEndpoint == nil || provider.TokenEndpoint.Host != "platform.claude.com" || len(provider.TokenEndpoint.Deny) != 3 {
		t.Fatal("native auth declarations lost in runtime profile")
	}
	summary := summarizeSpecDocument(doc)
	if len(summary.Providers[0].AuthSecrets) != 3 {
		t.Fatal("native auth declarations lost in capability summary")
	}
}

func TestAuthSecretsValidation(t *testing.T) {
	paths := realRepoPaths(t)
	baseline, err := LoadSpec(paths, "claude", KindSandbox)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*specDocument){
		"outside file":      func(d *specDocument) { d.Providers[0].AuthSecrets[0].File = "../outside" },
		"undeclared file":   func(d *specDocument) { d.Providers[0].AuthSecrets[0].File = "other.json" },
		"unknown codec":     func(d *specDocument) { d.Providers[0].AuthSecrets[0].Codec = "unknown" },
		"invalid pointer":   func(d *specDocument) { d.Providers[0].AuthSecrets[0].Fields[0].Pointer = "invalid" },
		"duplicate pointer": func(d *specDocument) { f := &d.Providers[0].AuthSecrets[0]; f.Fields = append(f.Fields, f.Fields[0]) },
		"unknown release":   func(d *specDocument) { d.Providers[0].AuthSecrets[0].Fields[0].Release = "missing" },
		"refresh release":   func(d *specDocument) { d.Providers[0].AuthSecrets[0].Fields[1].Release = "claude-code-oauth-token" },
		"API shape": func(d *specDocument) {
			d.Providers[0].AuthSecrets[1].Fields[0].Placeholder = &model.SecretPlaceholderConfig{}
		},
		"refresh invalid shape": func(d *specDocument) {
			d.Providers[0].AuthSecrets[0].Fields[1].Placeholder = &model.SecretPlaceholderConfig{}
		},
		"insufficient entropy":    func(d *specDocument) { d.Providers[0].AuthSecrets[0].Fields[0].Placeholder.Random.Length = 36 },
		"endpoint wildcard":       func(d *specDocument) { d.Providers[0].TokenEndpoint.Host = "*.claude.com" },
		"endpoint query":          func(d *specDocument) { d.Providers[0].TokenEndpoint.Path = "/v1/oauth/token?other=1" },
		"endpoint traversal":      func(d *specDocument) { d.Providers[0].TokenEndpoint.Path = "/v1/../oauth/token" },
		"endpoint body":           func(d *specDocument) { d.Providers[0].TokenEndpoint.Body = "protobuf" },
		"endpoint missing access": func(d *specDocument) { delete(d.Providers[0].TokenEndpoint.Fields, "access") },
		"deny wildcard":           func(d *specDocument) { d.Providers[0].TokenEndpoint.Deny[0].Host = "*.anthropic.com" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(baseline)
			if err != nil {
				t.Fatal(err)
			}
			var doc specDocument
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			mutate(&doc)
			if err := validateAuthSecrets(doc, "fixture.yaml"); err == nil {
				t.Fatal("accepted invalid native auth declaration")
			}
		})
	}
}

func TestCodexNativeAuthDeclarations(t *testing.T) {
	profile, err := LoadProfile(realRepoPaths(t), "codex")
	if err != nil {
		t.Fatal(err)
	}
	p := profile.Providers[0]
	if p.TokenEndpoint == nil || p.TokenEndpoint.Body != "either" || p.TokenEndpoint.Fields["id"] != "id_token" || len(p.AuthSecrets) != 1 || len(p.AuthSecrets[0].Fields) != 4 {
		t.Fatal("Codex native declarations missing")
	}
}
