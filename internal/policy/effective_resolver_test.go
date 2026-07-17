// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package policy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/network"
)

func TestEffectiveResolverResolveBasic(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "project")
	toolsDir, allowlistsDir := preparePolicyTestTool(t)

	resolver := NewEffectiveResolver(model.Paths{
		ToolsDir:      toolsDir,
		AllowlistsDir: allowlistsDir,
	}, home)

	result, err := resolver.Resolve(ResolveInput{
		ProjectDir:  projectDir,
		ProjectHash: "p1",
		Tool:        "codex",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	wantAllowlist := filepath.Join(toolsDir, "codex", model.GatewayAllowlistFilename)
	if result.AllowlistPath != wantAllowlist {
		t.Fatalf("unexpected allowlist path: want %q, got %q", wantAllowlist, result.AllowlistPath)
	}
	if result.GlobalPolicy.Mode != "" {
		t.Fatalf("expected empty global policy, got %+v", result.GlobalPolicy)
	}
	if result.ProjectPolicy.Mode != "" {
		t.Fatalf("expected empty project policy, got %+v", result.ProjectPolicy)
	}
}

func TestEffectiveResolverResolveMergesProjectPolicy(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "project")
	toolsDir, allowlistsDir := preparePolicyTestTool(t)

	if err := network.SavePolicy(config.HostNetworkPolicyPath(home), network.Policy{
		Domains: network.PolicyDomains{Global: []string{"global.example.com"}},
	}); err != nil {
		t.Fatalf("save global policy: %v", err)
	}
	if err := network.SavePolicy(config.HostProjectNetworkPolicyPath(home, "p1"), network.Policy{
		Domains: network.PolicyDomains{
			Global: []string{"project.example.com"},
			Tools: map[string][]string{
				"codex": {"tool.project.example.com"},
			},
		},
	}); err != nil {
		t.Fatalf("save project policy: %v", err)
	}

	resolver := NewEffectiveResolver(model.Paths{
		ToolsDir:      toolsDir,
		AllowlistsDir: allowlistsDir,
	}, home)
	result, err := resolver.Resolve(ResolveInput{
		ProjectDir:  projectDir,
		ProjectHash: "p1",
		Tool:        "codex",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if !slices.Contains(result.Effective.Domains, "global.example.com") {
		t.Fatalf("expected global policy domain in effective policy, got %v", result.Effective.Domains)
	}
	if !slices.Contains(result.Effective.Domains, "project.example.com") {
		t.Fatalf("expected project policy domain in effective policy, got %v", result.Effective.Domains)
	}
	if !slices.Contains(result.Effective.ToolDomains["codex"], "tool.project.example.com") {
		t.Fatalf("expected project tool domain in effective policy, got %v", result.Effective.ToolDomains["codex"])
	}
}

func TestEffectiveResolverResolveSkipsProjectPolicyWhenProjectDirEmpty(t *testing.T) {
	home := t.TempDir()
	toolsDir, allowlistsDir := preparePolicyTestTool(t)

	projectPolicyPath := config.HostProjectNetworkPolicyPath(home, "p1")
	if err := os.MkdirAll(filepath.Dir(projectPolicyPath), 0o750); err != nil {
		t.Fatalf("mkdir project override dir: %v", err)
	}
	if err := os.WriteFile(projectPolicyPath, []byte("{ invalid-json\n"), 0o644); err != nil {
		t.Fatalf("write invalid project policy: %v", err)
	}
	if err := network.SavePolicy(config.HostNetworkPolicyPath(home), network.Policy{
		Domains: network.PolicyDomains{Global: []string{"global.example.com"}},
	}); err != nil {
		t.Fatalf("save global policy: %v", err)
	}

	resolver := NewEffectiveResolver(model.Paths{
		ToolsDir:      toolsDir,
		AllowlistsDir: allowlistsDir,
	}, home)
	result, err := resolver.Resolve(ResolveInput{
		ProjectDir:  "",
		ProjectHash: "p1",
		Tool:        "codex",
	})
	if err != nil {
		t.Fatalf("resolve with empty project dir should skip project policy load: %v", err)
	}
	if result.ProjectPolicy.Mode != "" || len(result.ProjectPolicy.Domains.Global) != 0 {
		t.Fatalf("expected empty project policy when project dir is empty, got %+v", result.ProjectPolicy)
	}
}

func TestEffectiveResolverResolvePropagatesGlobalPolicyLoadError(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "project")
	toolsDir, allowlistsDir := preparePolicyTestTool(t)

	if err := os.MkdirAll(filepath.Dir(config.HostNetworkPolicyPath(home)), 0o750); err != nil {
		t.Fatalf("mkdir global policy dir: %v", err)
	}
	if err := os.WriteFile(config.HostNetworkPolicyPath(home), []byte("{ invalid-json\n"), 0o644); err != nil {
		t.Fatalf("write invalid global policy: %v", err)
	}

	resolver := NewEffectiveResolver(model.Paths{
		ToolsDir:      toolsDir,
		AllowlistsDir: allowlistsDir,
	}, home)
	_, err := resolver.Resolve(ResolveInput{
		ProjectDir:  projectDir,
		ProjectHash: "p1",
		Tool:        "codex",
	})
	if err == nil {
		t.Fatal("expected resolve to fail for invalid global policy")
	}
	if !strings.Contains(err.Error(), "load global policy") {
		t.Fatalf("expected load global policy context in error, got: %v", err)
	}
}

func TestEffectiveResolverResolveRequiresTool(t *testing.T) {
	toolsDir, allowlistsDir := preparePolicyTestTool(t)
	resolver := NewEffectiveResolver(model.Paths{
		ToolsDir:      toolsDir,
		AllowlistsDir: allowlistsDir,
	}, t.TempDir())

	_, err := resolver.Resolve(ResolveInput{ProjectDir: "/work/project", ProjectHash: "p1"})
	if err == nil || !strings.Contains(err.Error(), "tool is required") {
		t.Fatalf("Resolve() error = %v, want tool is required", err)
	}
}

func TestEffectiveResolverResolveRequiresProjectHashWhenProjectDirSet(t *testing.T) {
	toolsDir, allowlistsDir := preparePolicyTestTool(t)
	resolver := NewEffectiveResolver(model.Paths{
		ToolsDir:      toolsDir,
		AllowlistsDir: allowlistsDir,
	}, t.TempDir())

	_, err := resolver.Resolve(ResolveInput{ProjectDir: "/work/project", Tool: "codex"})
	if err == nil || !strings.Contains(err.Error(), "project hash is required") {
		t.Fatalf("Resolve() error = %v, want project hash is required", err)
	}
}

func TestEffectiveResolverResolveUsesGatewayOverrideAllowlist(t *testing.T) {
	home := t.TempDir()
	toolsDir, allowlistsDir := preparePolicyTestTool(t)
	overrideDir := config.HostProjectGatewayAllowlistsDir(home, "p1")
	if err := os.MkdirAll(overrideDir, 0o750); err != nil {
		t.Fatalf("mkdir override dir: %v", err)
	}
	overridePath := filepath.Join(overrideDir, "codex.conf")
	if err := os.WriteFile(overridePath, []byte("server=/override.example.com/8.8.8.8\n"), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	resolver := NewEffectiveResolver(model.Paths{
		ToolsDir:      toolsDir,
		AllowlistsDir: allowlistsDir,
	}, home)
	result, err := resolver.Resolve(ResolveInput{
		ProjectDir:  "/work/project",
		ProjectHash: "p1",
		Tool:        "codex",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.AllowlistPath != overridePath {
		t.Fatalf("allowlist path = %q, want %q", result.AllowlistPath, overridePath)
	}
}

func TestEffectiveResolverResolveLoadsSpecAllowedDomains(t *testing.T) {
	home := t.TempDir()
	toolsDir := filepath.Join(t.TempDir(), "tools")
	allowlistsDir := filepath.Join(t.TempDir(), "allowlists")
	if err := os.MkdirAll(filepath.Join(toolsDir, "speccy"), 0o750); err != nil {
		t.Fatalf("mkdir tool dir: %v", err)
	}
	if err := os.MkdirAll(allowlistsDir, 0o750); err != nil {
		t.Fatalf("mkdir allowlists dir: %v", err)
	}
	spec := `schemaVersion: "1"
kind: sandbox
name: speccy
sandbox:
  entrypoint: { run: [speccy] }
  configDir: .speccy
network:
  allowedDomains: [spec.example.com]
`
	if err := os.WriteFile(filepath.Join(toolsDir, "speccy", config.SpecFilename), []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	resolver := NewEffectiveResolver(model.Paths{
		ToolsDir:      toolsDir,
		AllowlistsDir: allowlistsDir,
	}, home)
	// No SpecAllowedDomains supplied on the input: the resolver must load the
	// tool's spec.yaml network.allowedDomains itself, matching session startup.
	result, err := resolver.Resolve(ResolveInput{Tool: "speccy"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !slices.Contains(result.Effective.Domains, "spec.example.com") {
		t.Fatalf("expected spec allowedDomains in effective policy, got %v", result.Effective.Domains)
	}
}

func TestEffectiveResolverResolveLoadsSpecDeniedDomains(t *testing.T) {
	home := t.TempDir()
	toolsDir := filepath.Join(t.TempDir(), "tools")
	allowlistsDir := filepath.Join(t.TempDir(), "allowlists")
	if err := os.MkdirAll(filepath.Join(toolsDir, "speccy"), 0o750); err != nil {
		t.Fatalf("mkdir tool dir: %v", err)
	}
	if err := os.MkdirAll(allowlistsDir, 0o750); err != nil {
		t.Fatalf("mkdir allowlists dir: %v", err)
	}
	spec := `schemaVersion: "1"
kind: sandbox
name: speccy
sandbox:
  entrypoint: { run: [speccy] }
  configDir: .speccy
network:
  allowedDomains: [example.com]
  deniedDomains: [tracking.example.com]
`
	if err := os.WriteFile(filepath.Join(toolsDir, "speccy", config.SpecFilename), []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	resolver := NewEffectiveResolver(model.Paths{
		ToolsDir:      toolsDir,
		AllowlistsDir: allowlistsDir,
	}, home)
	// No Spec*Domains supplied on the input: the resolver must load the tool's
	// spec.yaml network.deniedDomains itself, matching session startup.
	result, err := resolver.Resolve(ResolveInput{Tool: "speccy"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !slices.Contains(result.Effective.DeniedDomains, "tracking.example.com") {
		t.Fatalf("expected spec deniedDomains in effective policy, got %v", result.Effective.DeniedDomains)
	}
	if slices.Contains(result.Effective.Domains, "tracking.example.com") {
		t.Fatalf("denied domain must not appear in allow Domains: %v", result.Effective.Domains)
	}
}

func preparePolicyTestTool(t *testing.T) (string, string) {
	t.Helper()
	toolsDir := filepath.Join(t.TempDir(), "tools")
	allowlistsDir := filepath.Join(t.TempDir(), "allowlists")
	if err := os.MkdirAll(filepath.Join(toolsDir, "codex"), 0o750); err != nil {
		t.Fatalf("mkdir tool dir: %v", err)
	}
	if err := os.MkdirAll(allowlistsDir, 0o750); err != nil {
		t.Fatalf("mkdir allowlists dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(toolsDir, "codex", "spec.yaml"), []byte("schemaVersion: \"1\"\nkind: sandbox\nname: codex\n"), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, "codex", model.GatewayAllowlistFilename), []byte("server=/api.openai.com/8.8.8.8\n"), 0o644); err != nil {
		t.Fatalf("write tool allowlist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(allowlistsDir, "base.conf"), []byte("server=/example.com/8.8.8.8\n"), 0o644); err != nil {
		t.Fatalf("write base allowlist: %v", err)
	}
	return toolsDir, allowlistsDir
}
