// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
	"enclave/internal/policy"
)

type gatewayApplyTestManager struct {
	gateways          []backend.GatewayInfo
	filter            backend.GatewayFilter
	reloadedIDs       []string
	reloadGenerations []string
}

func (m *gatewayApplyTestManager) ListGateways(_ context.Context, filter backend.GatewayFilter) ([]backend.GatewayInfo, error) {
	m.filter = filter
	var matched []backend.GatewayInfo
	for _, gateway := range m.gateways {
		if filter.Tool != "" && gateway.Tool != filter.Tool {
			continue
		}
		if filter.ProjectHash != "" && gateway.ProjectHash != filter.ProjectHash {
			continue
		}
		if filter.WorkspaceID != "" && gateway.WorkspaceID != filter.WorkspaceID {
			continue
		}
		matched = append(matched, gateway)
	}
	return matched, nil
}

func (m *gatewayApplyTestManager) VerifyGatewayConfigMount(context.Context, string, string) error {
	return nil
}

func (m *gatewayApplyTestManager) ReloadGatewayNetwork(_ context.Context, id string, generation string) error {
	m.reloadedIDs = append(m.reloadedIDs, id)
	m.reloadGenerations = append(m.reloadGenerations, generation)
	return nil
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

func TestDiscoverAndApplyGatewayTargetsIncludesAllTaggedMembers(t *testing.T) {
	home := t.TempDir()
	memberA := t.TempDir()
	memberB := t.TempDir()
	project := model.Project{Dir: memberA, RealDir: memberA, Hash: "shared123456"}
	manager := &gatewayApplyTestManager{gateways: []backend.GatewayInfo{
		{ID: "gateway-a", Tool: "codex", ProjectHash: project.Hash, ProjectDir: memberA, WorkspaceID: "workspace-a"},
		{ID: "gateway-b", Tool: "codex", ProjectHash: project.Hash, ProjectDir: memberB, WorkspaceID: "workspace-b"},
		{ID: "other-tool", Tool: "claude", ProjectHash: project.Hash, ProjectDir: memberB, WorkspaceID: "workspace-b"},
		{ID: "other-project", Tool: "codex", ProjectHash: "other123456", ProjectDir: memberB, WorkspaceID: "workspace-b"},
	}}
	paths := model.Paths{
		ToolsDir:      filepath.Join(home, "tools"),
		AllowlistsDir: filepath.Join(home, "allowlists"),
	}
	input := &CommandInput{
		Ctx:     NewAppContext(paths, project),
		Options: model.Options{RunOptions: model.RunOptions{Tool: "codex"}},
	}

	targets, _, err := discoverGatewayTargets(input, manager, false)
	if err != nil {
		t.Fatalf("discover gateway targets: %v", err)
	}
	if manager.filter.ProjectHash != project.Hash || manager.filter.Tool != "codex" {
		t.Fatalf("unexpected current-scope filter: %+v", manager.filter)
	}
	if manager.filter.WorkspaceID != "" {
		t.Fatalf("current-scope filter retained physical workspace %q", manager.filter.WorkspaceID)
	}
	if len(targets) != 2 {
		t.Fatalf("discovered %d tagged-member gateways, want 2", len(targets))
	}

	policyCtx := currentPolicyContext{
		Home:     home,
		Project:  project,
		Resolver: policy.NewEffectiveResolver(paths, home),
	}
	outcomes, applied, failed := applyGatewayTargets(policyCtx, manager, targets)
	if len(outcomes) != 2 || applied != 2 || failed != 0 {
		t.Fatalf("apply result: outcomes=%d applied=%d failed=%d", len(outcomes), applied, failed)
	}
	if len(manager.reloadedIDs) != 2 || manager.reloadedIDs[0] != "gateway-a" || manager.reloadedIDs[1] != "gateway-b" {
		t.Fatalf("reloaded gateways = %v, want [gateway-a gateway-b]", manager.reloadedIDs)
	}
	for i, generation := range manager.reloadGenerations {
		if generation == "" {
			t.Fatalf("reload %d used an empty generation", i)
		}
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
