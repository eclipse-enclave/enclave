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
)

func TestSpecDocumentRoundTrip(t *testing.T) {
	src := []byte(`
schemaVersion: "1"
kind: sandbox
name: claude
displayName: Claude Code
description: Anthropic's Claude coding agent
sandbox:
  image: debian:trixie-slim
  aiFilename: CLAUDE.md
  entrypoint: { run: [claude] }
  configDir: .claude
  skillsDir: .claude/skills
  yoloFlag: --dangerously-skip-permissions
  yoloEnabled: true
  continueArgs: [--continue]
  resumeArgs: [--resume]
  passthroughPaths: [agents/, commands/, settings.json]
credentials:
  sources:
    anthropic: { env: [ANTHROPIC_API_KEY] }
    claude-code-oauth: { env: [CLAUDE_CODE_OAUTH_TOKEN], apiKey: false }
network:
  serviceDomains: { api.anthropic.com: anthropic, "*.anthropic.com": anthropic }
  serviceAuth: { anthropic: { headerName: x-api-key, valueFormat: "%s" } }
  allowedDomains: [api.anthropic.com]
providers:
  - name: anthropic
    credentials: [anthropic, claude-code-oauth]
    authFiles: [config.json, .credentials.json]
    authSession:
      mode: any
      checks: [{ file: .credentials.json, type: file_exists }]
    securestorageDirEnv: CLAUDE_SECURESTORAGE_CONFIG_DIR
    oauthPorts: [{ port: "1455" }]
ports:
  - { container: 3000, publish: true, label: "Dev Server" }
`)
	var doc specDocument
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.SchemaVersion != "1" || doc.Kind != KindSandbox || doc.Name != "claude" {
		t.Fatalf("identity mismatch: %+v", doc)
	}
	if doc.Sandbox == nil || doc.Sandbox.ConfigDir != ".claude" || doc.Sandbox.YoloFlag == "" {
		t.Fatalf("sandbox mapping wrong: %+v", doc.Sandbox)
	}
	if doc.Sandbox.YoloEnabled == nil || *doc.Sandbox.YoloEnabled != true {
		t.Fatalf("yoloEnabled pointer not captured")
	}
	oauth := doc.Credentials.Sources["claude-code-oauth"]
	if oauth.APIKey == nil || *oauth.APIKey != false {
		t.Fatalf("apiKey:false not captured: %+v", oauth)
	}
	if doc.Network.ServiceAuth["anthropic"].ValueFormat != "%s" {
		t.Fatalf("serviceAuth valueFormat wrong")
	}
	if len(doc.Providers) != 1 || doc.Providers[0].OAuthPorts[0].Port != "1455" {
		t.Fatalf("provider mapping wrong: %+v", doc.Providers)
	}
	if len(doc.Ports) != 1 || doc.Ports[0].Container != 3000 {
		t.Fatalf("ports mapping wrong: %+v", doc.Ports)
	}
}
