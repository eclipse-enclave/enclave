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

func TestLoadPolicyMissingFile(t *testing.T) {
	p, err := LoadPolicy("/nonexistent/path/network.jsonc")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if p.Mode != "" {
		t.Fatalf("expected empty mode, got: %s", p.Mode)
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.jsonc")

	original := Policy{
		Mode: "restricted",
		Domains: PolicyDomains{
			Global: []string{"example.com", "api.example.com"},
			Tools:  map[string][]string{"claude": {"anthropic.com"}},
		},
		Advanced: PolicyAdvanced{
			Resolvers: []string{"8.8.8.8"},
		},
	}

	if err := SavePolicy(path, original); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if loaded.Mode != original.Mode {
		t.Fatalf("mode: got %s, want %s", loaded.Mode, original.Mode)
	}
	if len(loaded.Domains.Global) != 2 {
		t.Fatalf("global domains: got %d, want 2", len(loaded.Domains.Global))
	}
	if loaded.Domains.Global[0] != "example.com" {
		t.Fatalf("global domain[0]: got %s, want example.com", loaded.Domains.Global[0])
	}
	if len(loaded.Domains.Tools["claude"]) != 1 {
		t.Fatalf("tool domains: got %d, want 1", len(loaded.Domains.Tools["claude"]))
	}
	if len(loaded.Advanced.Resolvers) != 1 || loaded.Advanced.Resolvers[0] != "8.8.8.8" {
		t.Fatalf("resolvers: got %v, want [8.8.8.8]", loaded.Advanced.Resolvers)
	}
}

func TestLoadPolicyWithComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.jsonc")

	content := `{
  // Network policy
  "mode": "unrestricted",
  "domains": {
    /* global domains */
    "global": ["example.com"]
  }
}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("load with comments failed: %v", err)
	}
	if p.Mode != "unrestricted" {
		t.Fatalf("mode: got %s, want unrestricted", p.Mode)
	}
	if len(p.Domains.Global) != 1 || p.Domains.Global[0] != "example.com" {
		t.Fatalf("domains: got %v, want [example.com]", p.Domains.Global)
	}
}

func TestSaveCreatesDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "dir", "network.jsonc")

	if err := SavePolicy(path, Policy{Mode: "restricted"}); err != nil {
		t.Fatalf("save to nested dir failed: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestMinimalPolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.jsonc")

	if err := SavePolicy(path, Policy{}); err != nil {
		t.Fatalf("save empty policy failed: %v", err)
	}

	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("load empty policy failed: %v", err)
	}
	if p.Mode != "" {
		t.Fatalf("expected empty mode, got %s", p.Mode)
	}
}

func TestInheritBoolPointers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.jsonc")

	f := false
	p := Policy{
		InheritToolAllowlist: &f,
		InheritGlobalPolicy:  &f,
	}
	if err := SavePolicy(path, p); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.InheritToolAllowlist == nil || *loaded.InheritToolAllowlist != false {
		t.Fatal("expected inherit_tool_allowlist = false")
	}
	if loaded.InheritGlobalPolicy == nil || *loaded.InheritGlobalPolicy != false {
		t.Fatal("expected inherit_global_policy = false")
	}
}
