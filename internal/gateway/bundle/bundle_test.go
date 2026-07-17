// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package bundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/network"
)

func TestRenderConfigBundleRejectsUnrestricted(t *testing.T) {
	_, err := renderConfigBundle(network.EffectivePolicy{Mode: "unrestricted"}, "codex")
	if err == nil {
		t.Fatal("expected unrestricted mode to be rejected")
	}
}

func TestRenderConfigBundleIncludesToolDomains(t *testing.T) {
	bundle, err := renderConfigBundle(network.EffectivePolicy{
		Mode:    "restricted",
		Domains: []string{"Example.com"},
		ToolDomains: map[string][]string{
			"codex": {"api.example.com"},
		},
		Resolvers: []string{"9.9.9.9"},
	}, "codex")
	if err != nil {
		t.Fatalf("render bundle: %v", err)
	}

	dnsmasq := string(bundle.dnsmasq)
	if !strings.Contains(dnsmasq, "server=/example.com/9.9.9.9") {
		t.Fatalf("expected global domain in dnsmasq config, got:\n%s", dnsmasq)
	}
	if !strings.Contains(dnsmasq, "server=/api.example.com/9.9.9.9") {
		t.Fatalf("expected tool domain in dnsmasq config, got:\n%s", dnsmasq)
	}
	if got := strings.TrimSpace(string(bundle.domains)); got != "api.example.com\nexample.com" {
		t.Fatalf("unexpected domains.txt contents: %q", got)
	}
}

func TestWriteConfigBundleWritesAllFiles(t *testing.T) {
	dir := t.TempDir()
	err := WriteConfigBundle(BundleWriteConfig{
		Dir: dir,
		Policy: network.EffectivePolicy{
			Mode:      "restricted",
			Domains:   []string{"example.com"},
			Resolvers: []string{"8.8.4.4"},
		},
		Tool: "codex",
	})
	if err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	paths := []string{
		filepath.Join(dir, "dnsmasq.conf"),
		filepath.Join(dir, "domains.txt"),
		filepath.Join(dir, "denied.txt"),
		filepath.Join(dir, "meta.json"),
	}
	for _, path := range paths {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("missing expected file %s: %v", path, statErr)
		}
	}

	metaPath := filepath.Join(dir, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var meta struct {
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse meta.json: %v", err)
	}
	if strings.TrimSpace(meta.Generation) == "" {
		t.Fatal("expected meta.json to include generation")
	}
}

func TestWriteConfigBundleWritesDeniedDomains(t *testing.T) {
	dir := t.TempDir()
	err := WriteConfigBundle(BundleWriteConfig{
		Dir: dir,
		Policy: network.EffectivePolicy{
			Mode:          "restricted",
			Domains:       []string{"example.com"},
			DeniedDomains: []string{"tracking.example.com"},
			Resolvers:     []string{"8.8.4.4"},
		},
		Tool: "codex",
	})
	if err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	denied, err := ReadDeniedDomains(filepath.Join(dir, "denied.txt"))
	if err != nil {
		t.Fatalf("read denied.txt: %v", err)
	}
	if len(denied) != 1 || denied[0] != "tracking.example.com" {
		t.Fatalf("expected [tracking.example.com] in denied.txt, got %v", denied)
	}
	// A bundle without denied domains yields an empty (parseable) denied list,
	// and ReadDeniedDomains tolerates a missing file (older bundles).
	empty := t.TempDir()
	got, err := ReadDeniedDomains(filepath.Join(empty, "denied.txt"))
	if err != nil {
		t.Fatalf("ReadDeniedDomains(missing) error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no denied domains for missing file, got %v", got)
	}
}

func TestRenderConfigBundleUsesNetworkRenderer(t *testing.T) {
	policy := network.EffectivePolicy{
		Mode:    "restricted",
		Domains: []string{"Example.com"},
		ToolDomains: map[string][]string{
			"codex": {"api.example.com"},
			"other": {"other.example.com"},
		},
		Resolvers: []string{"9.9.9.9"},
	}
	bundle, err := renderConfigBundle(policy, "codex")
	if err != nil {
		t.Fatalf("render bundle: %v", err)
	}
	preview, err := network.RenderDnsmasqConfigForTool(policy, "codex", "9.9.9.9")
	if err != nil {
		t.Fatalf("render preview: %v", err)
	}
	if string(bundle.dnsmasq) != preview {
		t.Fatalf("bundle dnsmasq != preview\nbundle:\n%s\npreview:\n%s", bundle.dnsmasq, preview)
	}
	if strings.Contains(preview, "other.example.com") {
		t.Fatalf("preview included domains for another tool:\n%s", preview)
	}
	if !strings.Contains(preview, "ipset=/api.example.com/enclave_allowed") {
		t.Fatalf("preview missing ipset lines:\n%s", preview)
	}
}
