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

func TestWarnReservedFields(t *testing.T) {
	doc := mustDoc(t, `
schemaVersion: "1"
kind: mixin
name: demo
agentContext: "hi"
commands:
  install:
    - { command: [apt-get, install, foo] }
  startup:
    - { command: [daemon], user: "1000" }
  initFiles:
    - { path: /etc/foo.conf, content: "x" }
`)
	var msgs []string
	warnReservedFields(doc, "demo", func(m string) { msgs = append(msgs, m) })
	joined := strings.Join(msgs, "\n")
	// Distinct, non-colliding substrings for the reserved fields still warned.
	// credentials.sources.<id>.file/.priority are now honored (Row 3),
	// network.deniedDomains is now honored (Row 7), commands.initFiles is now
	// honored (Row 4), commands.startup is now honored (Row 5), and
	// commands.install is now honored for mixins (Row 9); none of those must
	// appear here.
	for _, want := range []string{
		"agentContext",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing warning for %q in:\n%s", want, joined)
		}
	}
	// commands.install is now honored for mixins, so this mixin doc must not be
	// warned even though it declares it.
	if strings.Contains(joined, "commands.install") {
		t.Fatalf("unexpected warning for honored mixin commands.install in:\n%s", joined)
	}
	// commands.initFiles is now honored, so it must not be warned even though the
	// doc still declares it.
	if strings.Contains(joined, "initFiles") {
		t.Fatalf("unexpected warning for honored commands.initFiles in:\n%s", joined)
	}
	// commands.startup is now honored, so it must not be warned even though the
	// doc still declares it.
	if strings.Contains(joined, "startup") {
		t.Fatalf("unexpected warning for honored commands.startup in:\n%s", joined)
	}
}

func TestWarnReservedFieldsWarnsAIFilename(t *testing.T) {
	// sandbox.aiFilename is reserved and intentionally deferred alongside its
	// partner agentContext (Row 6 dropped). It must warn symmetrically rather
	// than being silently ignored.
	doc := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: svc
sandbox: { aiFilename: CLAUDE.md, entrypoint: { run: [svc] } }
`)
	var msgs []string
	warnReservedFields(doc, "svc", func(m string) { msgs = append(msgs, m) })
	joined := strings.Join(msgs, "\n")
	if !strings.Contains(joined, "sandbox.aiFilename") {
		t.Fatalf("missing warning for sandbox.aiFilename in:\n%s", joined)
	}
}

func TestWarnReservedFieldsCommandsInstallSandboxOnly(t *testing.T) {
	// commands.install is honored for mixins (Row 9) but not for sandbox tools,
	// which must use an install.sh sidecar. Warn only for the sandbox kind.
	install := `
commands:
  install:
    - { command: [apt-get, install, foo] }
`
	sandbox := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: svc
sandbox: { entrypoint: { run: [svc] } }`+install)
	var sandboxMsgs []string
	warnReservedFields(sandbox, "svc", func(m string) { sandboxMsgs = append(sandboxMsgs, m) })
	if !strings.Contains(strings.Join(sandboxMsgs, "\n"), "commands.install") {
		t.Fatalf("sandbox commands.install must warn, got: %v", sandboxMsgs)
	}

	mixin := mustDoc(t, `
schemaVersion: "1"
kind: mixin
name: feat`+install)
	var mixinMsgs []string
	warnReservedFields(mixin, "feat", func(m string) { mixinMsgs = append(mixinMsgs, m) })
	if strings.Contains(strings.Join(mixinMsgs, "\n"), "commands.install") {
		t.Fatalf("mixin commands.install is honored and must not warn, got: %v", mixinMsgs)
	}
}

func TestWarnReservedFieldsWarnsImage(t *testing.T) {
	// sandbox.image is a hint only: enclave keeps its own base image. Warn so
	// it is not a silent no-op.
	doc := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: svc
sandbox: { image: some/base:latest, entrypoint: { run: [svc] } }
`)
	var msgs []string
	warnReservedFields(doc, "svc", func(m string) { msgs = append(msgs, m) })
	if !strings.Contains(strings.Join(msgs, "\n"), "sandbox.image") {
		t.Fatalf("missing warning for sandbox.image in: %v", msgs)
	}
}

func TestWarnReservedFieldsHonorsCredentialFileAndPriority(t *testing.T) {
	// file / priority are honored (Row 3), so a credentials source using them
	// must not emit any reserved-field warning.
	doc := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: svc
sandbox: { entrypoint: { run: [svc] } }
credentials:
  sources:
    svc: { env: [X], file: { path: ~/x.json, parser: "json:a.b" }, priority: file-first }
`)
	var msgs []string
	warnReservedFields(doc, "svc", func(m string) { msgs = append(msgs, m) })
	if len(msgs) != 0 {
		t.Fatalf("expected no warnings for honored file/priority, got: %v", msgs)
	}
}

func TestWarnReservedFieldsSilentWhenClean(t *testing.T) {
	doc := mustDoc(t, `
schemaVersion: "1"
kind: sandbox
name: clean
sandbox: { entrypoint: { run: [clean] } }
network: { allowedDomains: [example.com], deniedDomains: [tracking.example.com] }
environment: { variables: { LOG: info }, proxyManaged: [MY_SERVICE_API_KEY] }
`)
	var msgs []string
	warnReservedFields(doc, "clean", func(m string) { msgs = append(msgs, m) })
	if len(msgs) != 0 {
		t.Fatalf("expected no warnings, got: %v", msgs)
	}
}
