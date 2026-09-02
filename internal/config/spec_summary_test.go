// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const summaryFeatureSpec = `schemaVersion: "1"
kind: mixin
name: foo
displayName: Foo
description: A test feature
needsRoot: true
defaultEnabled: false
aptPackages:
  - gdb
  - strace
configDir: .config/foo
authFiles:
  - hosts.yml
sandbox:
  entrypoint:
    run: [foo, --serve]
  passthroughPaths:
    - agents/
  hostConfigDir: .foo
  hostCredentialsFile: .credentials.json
  hostOauthJson: .foo.json
  yoloFlag: --dangerously-skip-permissions
  continueArgs: [resume, --last]
  resumeArgs: [resume]
postStart:
  openIDE: theia
commands:
  install:
    - command: apt-get install -y foo
      user: root
  startup:
    - command: foo --serve
      description: start foo
  initFiles:
    - path: ${WORKDIR}/.foo.yaml
      content: "{}"
      mode: "0600"
network:
  allowedDomains:
    - api.acme.com
  deniedDomains:
    - evil.example
  serviceDomains:
    cdn.acme.com: acme
  serviceAuth:
    acme:
      headerName: X-Acme-Token
      hosts: [api.acme.com]
environment:
  variables:
    NODE_TLS_REJECT_UNAUTHORIZED: "0"
  proxyManaged:
    - ACME_TOKEN
credentials:
  sources:
    acme:
      env:
        - ACME_TOKEN
      file:
        path: ~/.acme/token
        parser: json
providers:
  - name: acme
    authFiles: [config.json]
    authSession:
      mode: any
      checks: [{ file: auth.json, type: file_exists }]
    oauthPorts:
      - { port: "1455" }
    securestorageDirEnv: ACME_SECURESTORAGE_DIR
ports:
  - container: 8080
    publish: true
`

func TestSummarizeSpecDir(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, SpecFilename), summaryFeatureSpec)

	summary, err := SummarizeSpecDir(dir)
	if err != nil {
		t.Fatalf("SummarizeSpecDir: %v", err)
	}
	if summary.Kind != KindMixin || summary.Name != "foo" {
		t.Fatalf("kind/name = %q/%q", summary.Kind, summary.Name)
	}
	if !summary.NeedsRoot {
		t.Error("NeedsRoot = false, want true")
	}
	if len(summary.InstallCommands) != 1 || summary.InstallCommands[0] != "(root) apt-get install -y foo" {
		t.Errorf("InstallCommands = %v, want the root-marked command text", summary.InstallCommands)
	}
	// The spec's startup entry carries a description; the summary projects the
	// command itself so a rewrite under an unchanged description still diffs.
	if len(summary.StartupCommands) != 1 || summary.StartupCommands[0] != "foo --serve" {
		t.Errorf("StartupCommands = %v, want the command text", summary.StartupCommands)
	}
	if len(summary.InitFiles) != 1 || summary.InitFiles[0].OnlyIfMissing {
		t.Errorf("InitFiles = %+v", summary.InitFiles)
	}
	// Mode and a stand-in for the content both decide what lands on disk at
	// every start, so both have to reach the summary.
	if summary.InitFiles[0].Mode != "0600" || summary.InitFiles[0].ContentDigest == "" {
		t.Errorf("InitFiles[0] = %+v, want the mode and a content digest", summary.InitFiles[0])
	}
	if len(summary.AllowedDomains) != 1 || summary.AllowedDomains[0] != "api.acme.com" {
		t.Errorf("AllowedDomains = %v", summary.AllowedDomains)
	}
	if len(summary.Ports) != 1 || summary.Ports[0] != (SpecPortSummary{Container: 8080, Publish: true}) {
		t.Errorf("Ports = %v, want the container port with publish carried alongside it", summary.Ports)
	}
	if len(summary.CredentialEnv) != 1 || summary.CredentialEnv[0] != "ACME_TOKEN" {
		t.Errorf("CredentialEnv = %v", summary.CredentialEnv)
	}
	if summary.DefaultEnabled == nil || *summary.DefaultEnabled {
		t.Errorf("DefaultEnabled = %v, want explicit false", summary.DefaultEnabled)
	}
	if len(summary.AptPackages) != 2 {
		t.Errorf("AptPackages = %v", summary.AptPackages)
	}
	if summary.EntrypointOverride != "foo --serve" {
		t.Errorf("EntrypointOverride = %q, want %q", summary.EntrypointOverride, "foo --serve")
	}
	if summary.ContinueArgs != "resume --last" || summary.ResumeArgs != "resume" {
		t.Errorf("session args = %q/%q, want the argv appended to the agent for --continue/--resume",
			summary.ContinueArgs, summary.ResumeArgs)
	}
	if summary.PostStartOpenIDE != "theia" {
		t.Errorf("PostStartOpenIDE = %q, want theia", summary.PostStartOpenIDE)
	}
	// The value is reported alongside the name: NODE_TLS_REJECT_UNAUTHORIZED=0
	// and =1 are different grants.
	if len(summary.EnvironmentVars) != 1 || summary.EnvironmentVars[0] != "NODE_TLS_REJECT_UNAUTHORIZED=0" {
		t.Errorf("EnvironmentVars = %v, want the value carried alongside the name", summary.EnvironmentVars)
	}
	if len(summary.CredentialSources) != 1 {
		t.Fatalf("CredentialSources = %v, want one entry", summary.CredentialSources)
	}
	cs := summary.CredentialSources[0]
	if cs.ID != "acme" || cs.FilePath != "~/.acme/token" || cs.FileParser != "json" {
		t.Errorf("CredentialSources[0] file fields = %+v", cs)
	}
	if cs.ReleaseHeader != "X-Acme-Token" || len(cs.ReleaseHosts) != 2 {
		t.Errorf("CredentialSources[0] release fields = %+v, want header X-Acme-Token and 2 hosts (cdn.acme.com + api.acme.com)", cs)
	}
	if len(summary.Providers) != 1 {
		t.Fatalf("Providers = %v, want one entry", summary.Providers)
	}
	provider := summary.Providers[0]
	if provider.Name != "acme" || len(provider.AuthFiles) != 1 || !provider.AuthSession || provider.AuthSessionMode != "any" {
		t.Errorf("Providers[0] = %+v", provider)
	}
	if len(provider.OAuthPorts) != 1 || provider.OAuthPorts[0] != "1455" {
		t.Errorf("Providers[0].OAuthPorts = %v", provider.OAuthPorts)
	}
	if provider.SecurestorageDirEnv != "ACME_SECURESTORAGE_DIR" {
		t.Errorf("Providers[0].SecurestorageDirEnv = %q", provider.SecurestorageDirEnv)
	}
	wantExposure := SpecHostExposure{
		PassthroughPaths:    []string{"agents/"},
		HostConfigDir:       ".foo",
		HostCredentialsFile: ".credentials.json",
		HostOAuthJSON:       ".foo.json",
		MixinConfigDir:      ".config/foo",
		MixinAuthFiles:      []string{"hosts.yml"},
	}
	if !reflect.DeepEqual(summary.HostExposure, wantExposure) {
		t.Errorf("HostExposure = %+v, want %+v", summary.HostExposure, wantExposure)
	}
	if summary.YoloFlag != "--dangerously-skip-permissions" {
		t.Errorf("YoloFlag = %q, want %q", summary.YoloFlag, "--dangerously-skip-permissions")
	}
	if !summary.YoloEnabled {
		t.Error("YoloEnabled = false, want true (an unset yoloEnabled defaults to true)")
	}
}

// TestSummarizeSpecDirYoloDisabled confirms an explicit yoloEnabled: false
// overrides the default-true behavior TestSummarizeSpecDir exercises above.
func TestSummarizeSpecDirYoloDisabled(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, SpecFilename), "schemaVersion: \"1\"\nkind: sandbox\nname: foo\n"+
		"sandbox:\n  yoloFlag: --dangerously-skip-permissions\n  yoloEnabled: false\n")

	summary, err := SummarizeSpecDir(dir)
	if err != nil {
		t.Fatalf("SummarizeSpecDir: %v", err)
	}
	if summary.YoloFlag != "--dangerously-skip-permissions" {
		t.Errorf("YoloFlag = %q, want %q", summary.YoloFlag, "--dangerously-skip-permissions")
	}
	if summary.YoloEnabled {
		t.Error("YoloEnabled = true, want false (explicit yoloEnabled: false)")
	}
}

func TestSummarizeSpecDirAcceptsJSON(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, SpecFilenameJSON), `{"schemaVersion":"1","kind":"sandbox","name":"bar"}`)

	summary, err := SummarizeSpecDir(dir)
	if err != nil {
		t.Fatalf("SummarizeSpecDir: %v", err)
	}
	if summary.Kind != KindSandbox || summary.Name != "bar" {
		t.Fatalf("kind/name = %q/%q", summary.Kind, summary.Name)
	}
}

func TestSummarizeSpecDirMissing(t *testing.T) {
	if _, err := SummarizeSpecDir(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func TestSummarizeSpecDirRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, SpecFilename), "schemaVersion: \"1\"\nkind: mixin\nname: foo\nbogusField: 1\n")
	if _, err := SummarizeSpecDir(dir); err == nil {
		t.Fatal("SummarizeSpecDir accepted an unknown field, want error")
	}
}

func TestSummarizeSpecDirRejectsSchemaVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, SpecFilename), "schemaVersion: \"2\"\nkind: mixin\nname: foo\n")
	_, err := SummarizeSpecDir(dir)
	if err == nil {
		t.Fatal("SummarizeSpecDir accepted a schemaVersion mismatch, want error")
	}
	if !strings.Contains(err.Error(), "schemaVersion") {
		t.Fatalf("err = %v, want it to mention schemaVersion", err)
	}
}
