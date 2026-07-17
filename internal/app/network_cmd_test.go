// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

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
