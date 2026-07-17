// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package network

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMergeDefaults(t *testing.T) {
	ep := Merge(MergeConfig{})
	if ep.Mode != "restricted" {
		t.Fatalf("expected restricted, got %s", ep.Mode)
	}
	if ep.ModeSource != "default" {
		t.Fatalf("expected default source, got %s", ep.ModeSource)
	}
	if !ep.InheritToolAllowlist {
		t.Fatal("expected inherit_tool_allowlist true by default")
	}
}

func TestMergeGlobalMode(t *testing.T) {
	ep := Merge(MergeConfig{
		GlobalPolicy: Policy{Mode: "unrestricted"},
	})
	if ep.Mode != "unrestricted" {
		t.Fatalf("expected unrestricted, got %s", ep.Mode)
	}
	if ep.ModeSource != "global" {
		t.Fatalf("expected global source, got %s", ep.ModeSource)
	}
}

func TestMergeProjectOverridesGlobal(t *testing.T) {
	ep := Merge(MergeConfig{
		GlobalPolicy:  Policy{Mode: "unrestricted"},
		ProjectPolicy: Policy{Mode: "restricted"},
	})
	if ep.Mode != "restricted" {
		t.Fatalf("expected restricted, got %s", ep.Mode)
	}
	if ep.ModeSource != "project" {
		t.Fatalf("expected project source, got %s", ep.ModeSource)
	}
}

func TestMergeGlobalPolicyDomains(t *testing.T) {
	ep := Merge(MergeConfig{
		GlobalPolicy: Policy{
			Domains: PolicyDomains{
				Global: []string{"example.com", "api.example.com"},
			},
		},
	})
	if len(ep.Domains) != 2 {
		t.Fatalf("expected 2 domains, got %d: %v", len(ep.Domains), ep.Domains)
	}
}

func TestMergeUnionDomains(t *testing.T) {
	ep := Merge(MergeConfig{
		GlobalPolicy: Policy{
			Domains: PolicyDomains{Global: []string{"a.com", "b.com"}},
		},
		ProjectPolicy: Policy{
			Domains: PolicyDomains{Global: []string{"b.com", "c.com"}},
		},
	})
	if len(ep.Domains) != 3 {
		t.Fatalf("expected 3 domains after union, got %d: %v", len(ep.Domains), ep.Domains)
	}
}

func TestMergeDedup(t *testing.T) {
	ep := Merge(MergeConfig{
		GlobalPolicy: Policy{
			Domains: PolicyDomains{Global: []string{"Example.COM", "example.com"}},
		},
	})
	if len(ep.Domains) != 1 {
		t.Fatalf("expected 1 domain after dedup, got %d: %v", len(ep.Domains), ep.Domains)
	}
}

func TestMergeIncludesSpecAllowedDomains(t *testing.T) {
	ep := Merge(MergeConfig{
		SpecAllowedDomains: []string{"Example.com", "cdn.example.com"},
	})
	if len(ep.Domains) != 2 {
		t.Fatalf("expected 2 domains, got %d: %v", len(ep.Domains), ep.Domains)
	}
	found := map[string]bool{}
	for _, d := range ep.Domains {
		found[d] = true
	}
	if !found["example.com"] {
		t.Fatalf("expected canonicalized example.com in %v", ep.Domains)
	}
	if !found["cdn.example.com"] {
		t.Fatalf("expected cdn.example.com in %v", ep.Domains)
	}
}

func TestMergeSpecAllowedDomainsAdditiveToPolicyDomains(t *testing.T) {
	ep := Merge(MergeConfig{
		GlobalPolicy: Policy{
			Domains: PolicyDomains{Global: []string{"policy.example.com"}},
		},
		SpecAllowedDomains: []string{"spec.example.com"},
	})
	if len(ep.Domains) != 2 {
		t.Fatalf("expected policy + spec domains to union, got %d: %v", len(ep.Domains), ep.Domains)
	}
}

func TestMergeIncludesSpecDeniedDomains(t *testing.T) {
	ep := Merge(MergeConfig{
		SpecAllowedDomains: []string{"example.com"},
		SpecDeniedDomains:  []string{"Tracking.example.com", "tracking.example.com"},
	})
	// Denied domains land in DeniedDomains, canonicalized + deduped...
	if len(ep.DeniedDomains) != 1 || ep.DeniedDomains[0] != "tracking.example.com" {
		t.Fatalf("expected [tracking.example.com] in DeniedDomains, got %v", ep.DeniedDomains)
	}
	// ...and must never leak into the allow set.
	for _, d := range ep.Domains {
		if d == "tracking.example.com" {
			t.Fatalf("denied domain leaked into allow Domains: %v", ep.Domains)
		}
	}
}

func TestMergeNormalizesWildcardAndPortSpecDomains(t *testing.T) {
	// The sbx kit format allows wildcard and host:port domain forms. These
	// must be accepted (mapped to the host-only apex) rather than aborting the
	// session at render time.
	ep := Merge(MergeConfig{
		SpecAllowedDomains: []string{"*.cdn.example.com", "ampcode.com:443"},
		SpecDeniedDomains:  []string{"*.telemetry.example.com"},
	})
	found := map[string]bool{}
	for _, d := range ep.Domains {
		found[d] = true
	}
	if !found["cdn.example.com"] {
		t.Fatalf("expected wildcard collapsed to cdn.example.com, got %v", ep.Domains)
	}
	if !found["ampcode.com"] {
		t.Fatalf("expected port stripped to ampcode.com, got %v", ep.Domains)
	}
	if len(ep.DeniedDomains) != 1 || ep.DeniedDomains[0] != "telemetry.example.com" {
		t.Fatalf("expected wildcard denied domain collapsed, got %v", ep.DeniedDomains)
	}
}

func TestMergeInheritFalse(t *testing.T) {
	f := false
	ep := Merge(MergeConfig{
		GlobalPolicy: Policy{InheritToolAllowlist: &f},
	})
	if ep.InheritToolAllowlist {
		t.Fatal("expected inherit_tool_allowlist false")
	}
}

func TestMergeBuiltInAllowlist(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(dir, "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "test.conf"), []byte("server=/builtin.example.com/8.8.8.8\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	allowlist := filepath.Join(dir, "allowlist.conf")
	if err := os.WriteFile(allowlist, []byte("conf-file=/etc/dnsmasq.allowlists/fragments/test.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ep := Merge(MergeConfig{
		BuiltInAllowlistPath: allowlist,
		AllowlistsDir:        dir,
		GlobalPolicy: Policy{
			Domains: PolicyDomains{Global: []string{"extra.com"}},
		},
	})

	found := false
	for _, d := range ep.Domains {
		if d == "builtin.example.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected builtin.example.com in domains: %v", ep.Domains)
	}
	if len(ep.Domains) != 2 {
		t.Fatalf("expected 2 domains, got %d: %v", len(ep.Domains), ep.Domains)
	}
}

func TestMergeToolDomains(t *testing.T) {
	ep := Merge(MergeConfig{
		GlobalPolicy: Policy{
			Domains: PolicyDomains{
				Tools: map[string][]string{"claude": {"anthropic.com"}},
			},
		},
		ProjectPolicy: Policy{
			Domains: PolicyDomains{
				Tools: map[string][]string{"claude": {"claude.ai", "anthropic.com"}},
			},
		},
	})
	if len(ep.ToolDomains["claude"]) != 2 {
		t.Fatalf("expected 2 tool domains after dedup, got %d: %v", len(ep.ToolDomains["claude"]), ep.ToolDomains["claude"])
	}
}
