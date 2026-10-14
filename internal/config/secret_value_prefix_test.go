// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"strings"
	"testing"
)

func TestServiceAuthRequiredValuePrefix(t *testing.T) {
	for _, prefix := range []string{"", "at-", "sk-ant-oat", "with space", "newline\n", "é", strings.Repeat("a", 257)} {
		t.Run(prefix, func(t *testing.T) {
			doc := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: tool
credentials:
  sources:
    token: {env: [TOKEN]}
network:
  serviceAuth:
    token: {headerName: authorization, hosts: [api.example.com], requiredValuePrefix: at-}
`)
			rule := doc.Network.ServiceAuth["token"]
			if rule.RequiredValuePrefix != "at-" {
				t.Fatal("prefix not parsed")
			}
			rule.RequiredValuePrefix = prefix
			doc.Network.ServiceAuth["token"] = rule
			secrets, err := validateAndNormalizeSecretConfigs(buildSecrets(doc))
			valid := prefix == "" || prefix == "at-" || prefix == "sk-ant-oat"
			if (err == nil) != valid {
				t.Fatalf("normalization error = %v", err)
			}
			if valid && secrets["token"].Release.HTTP.RequiredValuePrefix != prefix {
				t.Fatal("prefix lost in mapping/normalization")
			}
		})
	}
}
