// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

// networkPreviewTestInput selects a tool with a spec domain, a feature with
// allowed, denied, and credential release domains, and an allow_domains entry.
func networkPreviewTestInput(t *testing.T) *CommandInput {
	t.Helper()
	root := t.TempDir()
	paths := model.Paths{
		ToolsDir:      filepath.Join(root, "tools"),
		FeaturesDir:   filepath.Join(root, "features"),
		AllowlistsDir: filepath.Join(root, "allowlists"),
	}
	writeAppFile(t, filepath.Join(paths.ToolsDir, "speccy", config.SpecFilename), `schemaVersion: "1"
kind: sandbox
name: speccy
sandbox:
  entrypoint: { run: [speccy] }
  configDir: .speccy
network:
  allowedDomains: [tool.example.com]
`, 0o644)
	writeAppFile(t, filepath.Join(paths.FeaturesDir, "svc", config.SpecFilename), `schemaVersion: "1"
kind: mixin
name: svc
defaultEnabled: false
credentials:
  sources:
    svc-token: { env: [SVC_TOKEN] }
network:
  allowedDomains: [svc.example.com]
  deniedDomains: [telemetry.svc.example.com]
  serviceDomains:
    api.svc.example.org: svc-token
  serviceAuth:
    svc-token: { headerName: authorization, valueFormat: "Bearer %s" }
`, 0o644)

	opts := config.DefaultOptions()
	opts.Tool = "speccy"
	opts.Features = []string{"+svc"}
	opts.AllowDomains = []string{"Adhoc.Example.com"}
	return configTestInput(t, paths, opts, nil)
}

func TestNetworkPrintReflectsSessionOptions(t *testing.T) {
	input := networkPreviewTestInput(t)

	var code int
	out := captureStdout(t, func() { code = runNetworkPrint(input) })
	if code != 0 {
		t.Fatalf("runNetworkPrint exit code = %d\n%s", code, out)
	}
	for _, want := range []string{
		"server=/tool.example.com/",
		"server=/svc.example.com/",
		"server=/api.svc.example.org/",
		"server=/adhoc.example.com/",
		"address=/telemetry.svc.example.com/",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("network print missing %q:\n%s", want, out)
		}
	}

	input.Options.AllowAllNetwork = true
	out = captureStdout(t, func() { code = runNetworkPrint(input) })
	if code != 0 || out != "# Mode: unrestricted\n" {
		t.Fatalf("network print with allow_all_network = %d %q, want the unrestricted marker", code, out)
	}
}

// Invalid options unrelated to the network must not hide running gateways
// from status, while print and diff still reject them.
func TestNetworkStatusFallsBackOnInvalidOptions(t *testing.T) {
	stubDockerPing(t, errors.New("engine down"))
	input := networkPreviewTestInput(t)
	input.Options.ProjectMount = "bogus"

	var code int
	out, errOut := captureOutput(t, func() { code = runNetworkStatus(input) })
	if code != 0 || !strings.Contains(out, "Runtime policy state:") {
		t.Fatalf("runNetworkStatus = %d, want the runtime section:\n%s", code, out)
	}
	if !strings.Contains(errOut, "Cannot preview the session policy for these options (--project-mount") {
		t.Fatalf("stderr = %q, want the preview fallback warning", errOut)
	}
	for name, run := range map[string]func(*CommandInput) int{"print": runNetworkPrint, "diff": runNetworkDiff} {
		captureOutput(t, func() { code = run(input) })
		if code != 1 {
			t.Fatalf("%s exit code = %d, want 1 for invalid options", name, code)
		}
	}
}

func TestHashGatewayBundleDirStableForContent(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		filepath.Base(model.GatewayConfigDNSMasqPath): "dnsmasq",
		filepath.Base(model.GatewayConfigDomainsPath): "example.com\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	first, err := hashGatewayBundleDir(dir)
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	second, err := hashGatewayBundleDir(dir)
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if first != second {
		t.Fatalf("expected stable hash, got %q and %q", first, second)
	}

	if err := os.WriteFile(filepath.Join(dir, filepath.Base(model.GatewayConfigDomainsPath)), []byte("changed.example.com\n"), 0o644); err != nil {
		t.Fatalf("rewrite domains: %v", err)
	}
	third, err := hashGatewayBundleDir(dir)
	if err != nil {
		t.Fatalf("third hash: %v", err)
	}
	if third == first {
		t.Fatal("expected hash to change when bundle content changes")
	}
}

func TestRunMutationRuntimeApplyNoApply(t *testing.T) {
	input := &CommandInput{Options: model.Options{RunOptions: model.RunOptions{NoApply: true}}}
	if exitCode := runMutationRuntimeApply(input); exitCode != 0 {
		t.Fatalf("expected no-apply mutation to succeed, got %d", exitCode)
	}
}

func TestResolveGatewayTargetProjectDirFallbacksToCurrent(t *testing.T) {
	current := model.Project{Dir: "/work/current", Hash: "p1"}
	target := backend.GatewayInfo{ID: "aaaaaaaaaaaa", ProjectHash: "p1"}
	got, err := resolveGatewayTargetProjectDir(current, target)
	if err != nil {
		t.Fatalf("resolve target project dir: %v", err)
	}
	if got != "/work/current" {
		t.Fatalf("expected current project dir fallback, got %q", got)
	}
}

func TestResolveGatewayTargetProjectDirRejectsMissingLabelForForeignTarget(t *testing.T) {
	current := model.Project{Dir: "/work/current", Hash: "p1"}
	target := backend.GatewayInfo{ID: "bbbbbbbbbbbb", ProjectHash: "other"}
	if _, err := resolveGatewayTargetProjectDir(current, target); err == nil {
		t.Fatal("expected error for foreign target without project-dir label")
	}
}

func TestReadGatewayBundleGeneration(t *testing.T) {
	dir := t.TempDir()
	metaPath := filepath.Join(dir, filepath.Base(model.GatewayConfigMetaPath))
	const generation = "2026-02-15T00:00:00.000000001Z"
	if err := os.WriteFile(metaPath, []byte("{\"generation\":\""+generation+"\"}\n"), 0o644); err != nil {
		t.Fatalf("write meta.json: %v", err)
	}
	got, err := readGatewayBundleGeneration(dir)
	if err != nil {
		t.Fatalf("read generation: %v", err)
	}
	if got != generation {
		t.Fatalf("unexpected generation: want %q, got %q", generation, got)
	}
}
