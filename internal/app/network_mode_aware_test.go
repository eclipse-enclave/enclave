// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/gateway/bundle"
	"enclave/internal/model"
	"enclave/internal/network"
	"enclave/internal/policy"
)

type fakeGatewayManager struct {
	verified []string
	reloaded []string
}

func (f *fakeGatewayManager) ListGateways(context.Context, backend.GatewayFilter) ([]backend.GatewayInfo, error) {
	return nil, nil
}

func (f *fakeGatewayManager) VerifyGatewayConfigMount(_ context.Context, id string, _ string) error {
	f.verified = append(f.verified, id)
	return nil
}

func (f *fakeGatewayManager) ReloadGatewayNetwork(_ context.Context, id string, _ string) error {
	f.reloaded = append(f.reloaded, id)
	return nil
}

type modeAwareFixture struct {
	resolverCtx  policyResolverContext
	restricted   backend.GatewayInfo
	unrestricted backend.GatewayInfo
}

// newModeAwareFixture has one gateway without a mode label, as restricted
// gateways are started, and one labelled unrestricted, of the same project/tool.
func newModeAwareFixture(t *testing.T) modeAwareFixture {
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
	home := filepath.Join(root, "home")
	projectDir := filepath.Join(root, "project")
	project := model.Project{Dir: projectDir, RealDir: projectDir, Hash: "p1"}
	base := backend.GatewayInfo{Tool: "speccy", ProjectHash: project.Hash, ProjectDir: projectDir}
	restricted := base
	restricted.ID = "restricted00000"
	restricted.Name = "speccy-a-gateway"
	unrestricted := base
	unrestricted.ID = "unrestricted000"
	unrestricted.Name = "speccy-b-gateway"
	unrestricted.NetworkMode = backend.NetworkModeUnrestricted
	return modeAwareFixture{
		resolverCtx: policyResolverContext{
			Home:     home,
			Project:  project,
			Resolver: policy.NewEffectiveResolver(paths, home),
		},
		restricted:   restricted,
		unrestricted: unrestricted,
	}
}

func (f modeAwareFixture) targets() []backend.GatewayInfo {
	return []backend.GatewayInfo{f.restricted, f.unrestricted}
}

func (f modeAwareFixture) setGlobalMode(t *testing.T, mode string) {
	t.Helper()
	if err := network.SavePolicy(network.GlobalPolicyPath(f.resolverCtx.Home), network.Policy{Mode: mode}); err != nil {
		t.Fatalf("save policy: %v", err)
	}
}

func targetLine(t *testing.T, out string, name string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, name) {
			return line
		}
	}
	t.Fatalf("no line for %s in output:\n%s", name, out)
	return ""
}

func TestNetworkApplySkipsUnrestrictedGateways(t *testing.T) {
	for _, scope := range []gatewayTargetScope{gatewayScopeProjectTool, gatewayScopeProject, gatewayScopeAllRunning} {
		f := newModeAwareFixture(t)
		manager := &fakeGatewayManager{}
		var out strings.Builder

		code := applyPolicyToTargets(&out, manager, f.resolverCtx, f.targets(), scope)

		if code != 0 {
			t.Fatalf("scope %d: exit code = %d, want 0\n%s", scope, code, out.String())
		}
		if !slices.Equal(manager.reloaded, []string{f.restricted.ID}) {
			t.Fatalf("scope %d: reloaded %v, want only the restricted gateway", scope, manager.reloaded)
		}
		if slices.Contains(manager.verified, f.unrestricted.ID) {
			t.Fatalf("scope %d: the unrestricted gateway must not be touched", scope)
		}
		if line := targetLine(t, out.String(), f.unrestricted.Name); !strings.Contains(line, "SKIPPED") || !strings.Contains(line, "unrestricted") {
			t.Fatalf("scope %d: unrestricted gateway line = %q, want a SKIPPED note naming unrestricted mode", scope, line)
		}
		if line := targetLine(t, out.String(), f.restricted.Name); !strings.Contains(line, "APPLIED") {
			t.Fatalf("scope %d: restricted gateway line = %q, want APPLIED", scope, line)
		}
		if !strings.Contains(out.String(), "Apply summary: 1 applied, 0 failed, 1 skipped") {
			t.Fatalf("scope %d: summary missing the skip:\n%s", scope, out.String())
		}
	}
}

func TestNetworkApplyWithoutSkipsKeepsTheSummary(t *testing.T) {
	f := newModeAwareFixture(t)
	var out strings.Builder

	code := applyPolicyToTargets(&out, &fakeGatewayManager{}, f.resolverCtx, []backend.GatewayInfo{f.restricted}, gatewayScopeProjectTool)

	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "Apply summary: 1 applied, 0 failed\n") {
		t.Fatalf("restricted-only summary changed:\n%s", out.String())
	}
}

func TestNetworkApplyNeverSwitchesARunningGatewayMode(t *testing.T) {
	f := newModeAwareFixture(t)
	f.setGlobalMode(t, model.NetworkModeUnrestricted)
	manager := &fakeGatewayManager{}
	var out strings.Builder

	code := applyPolicyToTargets(&out, manager, f.resolverCtx, f.targets(), gatewayScopeProject)

	if code == 0 {
		t.Fatalf("expected the restricted gateway to refuse an unrestricted policy\n%s", out.String())
	}
	if len(manager.reloaded) != 0 {
		t.Fatalf("reloaded %v, want no gateway reloaded", manager.reloaded)
	}
	if line := targetLine(t, out.String(), f.restricted.Name); !strings.Contains(line, "FAILED") {
		t.Fatalf("restricted gateway line = %q, want FAILED", line)
	}
	if line := targetLine(t, out.String(), f.unrestricted.Name); !strings.Contains(line, "SKIPPED") {
		t.Fatalf("unrestricted gateway line = %q, want SKIPPED", line)
	}
}

func TestNetworkStatusShowsUnrestrictedGateways(t *testing.T) {
	f := newModeAwareFixture(t)
	resolved, err := f.resolverCtx.Resolver.Resolve(policy.ResolveInput{
		ProjectDir:  f.restricted.ProjectDir,
		ProjectHash: f.restricted.ProjectHash,
		Tool:        f.restricted.Tool,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := bundle.WriteConfigBundle(bundle.BundleWriteConfig{
		Dir:    config.HostProjectGatewayConfigDir(f.resolverCtx.Home, f.restricted.ProjectHash, f.restricted.Tool),
		Policy: resolved.Effective,
		Tool:   f.restricted.Tool,
	}); err != nil {
		t.Fatalf("write restricted bundle: %v", err)
	}
	manager := &fakeGatewayManager{}
	var out strings.Builder

	printGatewayRuntimeStates(&out, manager, f.resolverCtx, f.targets())

	line := targetLine(t, out.String(), f.unrestricted.Name)
	if !strings.Contains(line, "UNRESTRICTED") || strings.Contains(line, "STALE") || strings.Contains(line, "IN-SYNC") {
		t.Fatalf("unrestricted gateway line = %q, want UNRESTRICTED", line)
	}
	if line := targetLine(t, out.String(), f.restricted.Name); !strings.Contains(line, "IN-SYNC") {
		t.Fatalf("restricted gateway line = %q, want IN-SYNC", line)
	}
	for _, want := range []string{"In sync:         1", "Needs apply:     0", "Unrestricted:    1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status counts missing %q:\n%s", want, out.String())
		}
	}
	if slices.Contains(manager.verified, f.unrestricted.ID) {
		t.Fatal("status must not compare the unrestricted gateway against the restricted bundle")
	}
}

func TestNetworkStatusWithoutUnrestrictedKeepsTheCounts(t *testing.T) {
	f := newModeAwareFixture(t)
	var out strings.Builder

	printGatewayRuntimeStates(&out, &fakeGatewayManager{}, f.resolverCtx, []backend.GatewayInfo{f.restricted})

	if strings.Contains(out.String(), "Unrestricted:") {
		t.Fatalf("restricted-only status gained an unrestricted count:\n%s", out.String())
	}
}

func TestNetworkLogReadsUnrestrictedGateways(t *testing.T) {
	f := newModeAwareFixture(t)
	other := f.unrestricted
	other.Tool = "codex"
	other.Name = "codex-c-gateway"
	other.SessionContainer = "codex-c"

	paths := networkLogPaths(f.resolverCtx.Home, []backend.GatewayInfo{f.restricted, other})
	want := config.HostProjectNetworkLogPath(f.resolverCtx.Home, other.ProjectHash, other.Tool)
	if !slices.Contains(paths, want) {
		t.Fatalf("log paths %v miss the unrestricted gateway's log %s", paths, want)
	}
	if got := filterGatewaysBySession([]backend.GatewayInfo{f.restricted, other}, "codex-c"); len(got) != 1 || got[0].ID != other.ID {
		t.Fatalf("--session did not select the unrestricted gateway: %+v", got)
	}
}
