// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"testing"

	"sigs.k8s.io/yaml"

	"enclave/internal/model"
)

func mustDoc(t *testing.T, src string) specDocument {
	t.Helper()
	var d specDocument
	if err := yaml.Unmarshal([]byte(src), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return d
}

func TestValidateServiceAuthMappings(t *testing.T) {
	t.Run("unmatched serviceAuth id fails", func(t *testing.T) {
		doc := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: codex
credentials:
  sources:
    openai-api-key: { env: [OPENAI_API_KEY] }
network:
  serviceAuth: { openai-api-kye: { headerName: authorization } }
`)
		if err := validateServiceAuthMappings(doc, "codex/spec.yaml"); err == nil {
			t.Fatal("expected error for serviceAuth id with no matching credentials.sources entry")
		}
	})

	t.Run("unmatched serviceDomains id fails", func(t *testing.T) {
		doc := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: codex
credentials:
  sources:
    openai-api-key: { env: [OPENAI_API_KEY] }
network:
  serviceDomains: { api.openai.com: openai-api-kye }
`)
		if err := validateServiceAuthMappings(doc, "codex/spec.yaml"); err == nil {
			t.Fatal("expected error for serviceDomains id with no matching credentials.sources entry")
		}
	})

	t.Run("matching ids pass", func(t *testing.T) {
		doc := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: codex
credentials:
  sources:
    openai-api-key: { env: [OPENAI_API_KEY] }
network:
  serviceDomains: { api.openai.com: openai-api-key }
  serviceAuth: { openai-api-key: { headerName: authorization } }
`)
		if err := validateServiceAuthMappings(doc, "codex/spec.yaml"); err != nil {
			t.Fatalf("unexpected error for well-formed mappings: %v", err)
		}
	})
}

func TestSpecToProfileSecretsSplit(t *testing.T) {
	doc := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: codex
sandbox:
  entrypoint: { run: [codex] }
  configDir: .codex
  yoloFlag: --dangerously-bypass-approvals-and-sandbox
  yoloEnabled: true
  continueArgs: [resume, --last]
credentials:
  sources:
    openai-api-key: { env: [OPENAI_API_KEY] }
network:
  serviceDomains: { api.openai.com: openai-api-key, "*.openai.com": openai-api-key }
  serviceAuth: { openai-api-key: { headerName: authorization, valueFormat: "Bearer %s" } }
providers:
  - name: openai-codex
    credentials: [openai-api-key]
    authFiles: [auth.json]
    oauthPorts: [{ port: "1455" }]
`)
	p := specToProfile(doc)
	if p.Command != "codex" || p.YoloFlag == "" || len(p.ContinueArgs) != 2 {
		t.Fatalf("sandbox->profile wrong: %+v", p)
	}
	sec, ok := p.Secrets["openai-api-key"]
	if !ok || len(sec.EnvVars) != 1 || sec.EnvVars[0] != "OPENAI_API_KEY" {
		t.Fatalf("secret env wrong: %+v", p.Secrets)
	}
	if sec.Release == nil || sec.Release.HTTP == nil {
		t.Fatalf("release not reconstructed")
	}
	if sec.Release.HTTP.Header != "authorization" || sec.Release.HTTP.Format != "Bearer %s" {
		t.Fatalf("release header/format wrong: %+v", sec.Release.HTTP)
	}
	if len(sec.Release.HTTP.Hosts) != 2 {
		t.Fatalf("expected 2 hosts, got %+v", sec.Release.HTTP.Hosts)
	}
	if len(p.Providers) != 1 || p.Providers[0].CredentialSecrets[0] != "openai-api-key" {
		t.Fatalf("provider credentials wrong: %+v", p.Providers)
	}
}

// TestSpecToProfileMultiServiceSameHost proves the enclave-native
// serviceAuth.hosts field: two services sharing the identical host set (which
// the host->single-service serviceDomains map cannot express) each get their
// own release with the correct hosts/header/format.
func TestSpecToProfileMultiServiceSameHost(t *testing.T) {
	doc := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: multi
sandbox: { entrypoint: { run: [multi] }, configDir: .multi }
credentials:
  sources:
    token-a: { env: [TOKEN_A] }
    token-b: { env: [TOKEN_B] }
network:
  serviceAuth:
    token-a: { headerName: private-token, hosts: [example.com, "*.example.com"] }
    token-b: { headerName: authorization, valueFormat: "Bearer %s", hosts: [example.com, "*.example.com"] }
`)
	p := specToProfile(doc)

	a := p.Secrets["token-a"]
	if a.Release == nil || a.Release.HTTP == nil {
		t.Fatalf("token-a release not built: %+v", a)
	}
	if a.Release.HTTP.Header != "private-token" || a.Release.HTTP.Format != "" {
		t.Fatalf("token-a header/format wrong: %+v", a.Release.HTTP)
	}
	if got := a.Release.HTTP.Hosts; len(got) != 2 || got[0] != "*.example.com" || got[1] != "example.com" {
		t.Fatalf("token-a hosts wrong: %+v", got)
	}

	b := p.Secrets["token-b"]
	if b.Release == nil || b.Release.HTTP == nil {
		t.Fatalf("token-b release not built: %+v", b)
	}
	if b.Release.HTTP.Header != "authorization" || b.Release.HTTP.Format != "Bearer %s" {
		t.Fatalf("token-b header/format wrong: %+v", b.Release.HTTP)
	}
	if got := b.Release.HTTP.Hosts; len(got) != 2 || got[0] != "*.example.com" || got[1] != "example.com" {
		t.Fatalf("token-b hosts wrong: %+v", got)
	}
}

// TestSpecToProfileServiceAuthHostsUnionsServiceDomains proves that when both
// a serviceDomains entry and a serviceAuth.hosts entry name hosts for the same
// service id, the release host list is the deduped, sorted union of both.
func TestSpecToProfileServiceAuthHostsUnionsServiceDomains(t *testing.T) {
	doc := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: unioned
sandbox: { entrypoint: { run: [unioned] }, configDir: .unioned }
credentials:
  sources:
    key: { env: [KEY] }
network:
  serviceDomains: { api.example.com: key }
  serviceAuth:
    key: { headerName: x-api-key, hosts: ["*.example.com", api.example.com] }
`)
	p := specToProfile(doc)
	sec := p.Secrets["key"]
	if sec.Release == nil || sec.Release.HTTP == nil {
		t.Fatalf("release not built: %+v", sec)
	}
	// api.example.com appears in both serviceDomains and serviceAuth.hosts; it
	// must be deduped. Sorted union is ["*.example.com", "api.example.com"].
	if got := sec.Release.HTTP.Hosts; len(got) != 2 || got[0] != "*.example.com" || got[1] != "api.example.com" {
		t.Fatalf("unioned hosts wrong: %+v", got)
	}
}

func TestSpecToProfileReleaseLessCredential(t *testing.T) {
	doc := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: claude
sandbox: { entrypoint: { run: [claude] }, configDir: .claude }
credentials:
  sources:
    claude-code-oauth: { env: [CLAUDE_CODE_OAUTH_TOKEN], apiKey: false }
`)
	p := specToProfile(doc)
	sec := p.Secrets["claude-code-oauth"]
	if sec.Release != nil {
		t.Fatalf("expected no release for oauth token, got %+v", sec.Release)
	}
	if sec.APIKey == nil || *sec.APIKey != false {
		t.Fatalf("apiKey:false not mapped: %+v", sec.APIKey)
	}
}

func TestSpecToProfileOAuthPortFlags(t *testing.T) {
	doc := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: codex
sandbox: { entrypoint: { run: [codex] }, configDir: .codex }
providers:
  - name: openai-codex
    oauthPorts: [{ port: "1455", autoHintWhenNoSession: false }]
`)
	p := specToProfile(doc)
	if len(p.Providers) != 1 || len(p.Providers[0].OAuthPorts) != 1 {
		t.Fatalf("oauth port not mapped: %+v", p.Providers)
	}
	op := p.Providers[0].OAuthPorts[0]
	if op.AutoHintWhenNoSession == nil || *op.AutoHintWhenNoSession != false {
		t.Fatalf("autoHintWhenNoSession not mapped: %+v", op.AutoHintWhenNoSession)
	}
}

func TestSpecToExtensionMixin(t *testing.T) {
	doc := mustDoc(t, `
schemaVersion: "1"
kind: mixin
name: github-cli
description: GitHub CLI (gh)
needsRoot: true
priority: 50
configDir: .config/gh
authFiles: [hosts.yml, config.yml]
credentials:
  sources:
    github-token: { env: [GH_TOKEN, GITHUB_TOKEN] }
network:
  serviceDomains: { api.github.com: github-token, "*.github.com": github-token }
  serviceAuth: { github-token: { headerName: authorization, valueFormat: "Bearer %s" } }
`)
	ext, state := specToExtension(doc)
	if ext.Type != model.ExtensionKindMixin || ext.Name != "github-cli" {
		t.Fatalf("kind->type wrong: %+v", ext)
	}
	if !ext.NeedsRoot || ext.ConfigDir != ".config/gh" || len(ext.AuthFiles) != 2 {
		t.Fatalf("mixin fields wrong: %+v", ext)
	}
	if !state.PrioritySet || ext.Priority != 50 {
		t.Fatalf("priority set-state wrong: %+v %+v", state, ext.Priority)
	}
	if _, ok := ext.Secrets["github-token"]; !ok {
		t.Fatalf("feature secret not mapped: %+v", ext.Secrets)
	}
}

func TestSpecToExtensionMapsInstallCommandUsers(t *testing.T) {
	doc := mustDoc(t, `
schemaVersion: "1"
kind: mixin
name: cmd-feat
commands:
  install:
    - { command: [apt-get, install, foo], user: root }
    - { command: "curl -fsSL https://example.com | sh" }
`)
	ext, _ := specToExtension(doc)
	got := ext.InstallCommandUsers
	// An explicit user is preserved; an omitted user defaults to "0" (root),
	// matching sbx §6.1.
	if len(got) != 2 || got[0] != "root" || got[1] != "0" {
		t.Fatalf("InstallCommandUsers = %#v, want [\"root\" \"0\"]", got)
	}
}

func TestSpecToExtensionDefaultFlagsSet(t *testing.T) {
	doc := mustDoc(t, `
schemaVersion: "1"
kind: mixin
name: some-feature
defaultEnabled: false
defaultIncluded: true
`)
	ext, state := specToExtension(doc)
	if !state.DefaultEnabledSet || !state.DefaultIncludedSet {
		t.Fatalf("expected both set-flags true, got %+v", state)
	}
	if ext.DefaultEnabled != false {
		t.Fatalf("defaultEnabled not carried: %+v", ext.DefaultEnabled)
	}
	if ext.DefaultIncluded != true {
		t.Fatalf("defaultIncluded not carried: %+v", ext.DefaultIncluded)
	}
}

func TestSpecToExtensionDefaultFlagsUnset(t *testing.T) {
	doc := mustDoc(t, `
schemaVersion: "1"
kind: mixin
name: some-feature
`)
	_, state := specToExtension(doc)
	if state.DefaultEnabledSet || state.DefaultIncludedSet {
		t.Fatalf("expected both set-flags false when omitted, got %+v", state)
	}
}
