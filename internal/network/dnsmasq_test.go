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
	"strings"
	"testing"
)

func TestExtractDomainsFromContent(t *testing.T) {
	content := []byte(`# Anthropic
server=/anthropic.com/8.8.8.8
server=/claude.ai/8.8.8.8
# comment
server=/example.com/1.1.1.1
`)
	domains := ExtractDomainsFromContent(content)
	if len(domains) != 3 {
		t.Fatalf("expected 3 domains, got %d: %v", len(domains), domains)
	}
	if domains[0] != "anthropic.com" {
		t.Fatalf("expected anthropic.com, got %s", domains[0])
	}
}

func TestExtractDomainsFromContentEmpty(t *testing.T) {
	domains := ExtractDomainsFromContent([]byte("# just comments\nno-resolv\n"))
	if len(domains) != 0 {
		t.Fatalf("expected 0 domains, got %d", len(domains))
	}
}

func TestExtractDomainsRecursive(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(dir, "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(fragDir, "github.conf"), []byte("server=/github.com/8.8.8.8\nserver=/api.github.com/8.8.8.8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "npm.conf"), []byte("server=/registry.npmjs.org/8.8.8.8\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	allowlist := filepath.Join(dir, "allowlist.conf")
	if err := os.WriteFile(allowlist, []byte(`no-resolv
address=/#/
conf-file=/etc/dnsmasq.allowlists/fragments/github.conf
conf-file=/etc/dnsmasq.allowlists/fragments/npm.conf
`), 0o644); err != nil {
		t.Fatal(err)
	}

	domains, err := ExtractDomainsRecursive(allowlist, dir)
	if err != nil {
		t.Fatalf("extract recursive failed: %v", err)
	}
	if len(domains) != 3 {
		t.Fatalf("expected 3 domains, got %d: %v", len(domains), domains)
	}
}

func TestExtractDomainsRecursiveDedup(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(dir, "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(fragDir, "a.conf"), []byte("server=/example.com/8.8.8.8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "b.conf"), []byte("server=/example.com/8.8.8.8\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	allowlist := filepath.Join(dir, "allowlist.conf")
	if err := os.WriteFile(allowlist, []byte("conf-file=/etc/dnsmasq.allowlists/fragments/a.conf\nconf-file=/etc/dnsmasq.allowlists/fragments/b.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	domains, err := ExtractDomainsRecursive(allowlist, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 1 {
		t.Fatalf("expected 1 domain after dedup, got %d: %v", len(domains), domains)
	}
}

func TestExtractDomainsRecursiveRejectsEscapingInclude(t *testing.T) {
	root := t.TempDir()
	allowlistsDir := filepath.Join(root, "allowlists")
	if err := os.MkdirAll(allowlistsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.conf"), []byte("server=/outside.example/8.8.8.8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	allowlist := filepath.Join(allowlistsDir, "allowlist.conf")
	if err := os.WriteFile(allowlist, []byte("server=/inside.example/8.8.8.8\nconf-file=../outside.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	domains, err := ExtractDomainsRecursive(allowlist, allowlistsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 1 || domains[0] != "inside.example" {
		t.Fatalf("escaping include contributed domains: %v", domains)
	}
}

func TestDomainToServerLine(t *testing.T) {
	got := DomainToServerLine("Example.COM", "1.1.1.1")
	want := "server=/example.com/1.1.1.1"
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestDomainToServerLineDefaultResolver(t *testing.T) {
	got := DomainToServerLine("example.com", "")
	if !strings.Contains(got, "8.8.8.8") {
		t.Fatalf("expected default resolver, got %s", got)
	}
}

func TestRenderDnsmasqConfigForToolMatchesGatewayLayout(t *testing.T) {
	ep := EffectivePolicy{
		Mode:    "restricted",
		Domains: []string{"example.com", "api.example.com"},
		ToolDomains: map[string][]string{
			"claude": {"anthropic.com"},
			"codex":  {"openai.com"},
		},
	}
	output, err := RenderDnsmasqConfigForTool(ep, "claude", "8.8.8.8")
	if err != nil {
		t.Fatalf("render dnsmasq: %v", err)
	}
	if !strings.Contains(output, "address=/#/") {
		t.Fatal("expected default block rule")
	}
	if !strings.Contains(output, "server=/example.com/8.8.8.8") {
		t.Fatal("expected example.com server line")
	}
	if !strings.Contains(output, "server=/anthropic.com/8.8.8.8") {
		t.Fatal("expected anthropic.com server line")
	}
	if strings.Contains(output, "openai.com") {
		t.Fatal("did not expect domains for another tool")
	}
	if !strings.Contains(output, "ipset=/anthropic.com/enclave_allowed") {
		t.Fatal("expected ipset line")
	}
}

func TestRenderDNSMasqConfigNoDenyByteIdentical(t *testing.T) {
	// Regression: the no-deny path must be byte-identical to the historical
	// allow-only output. If this drifts, allow-only bundles (and their golden
	// tests) drift too.
	got := RenderDNSMasqConfig([]string{"api.example.com", "example.com"}, nil, "8.8.8.8")
	want := "# Generated by enclave\n" +
		"no-resolv\n" +
		"no-poll\n" +
		"address=/#/\n" +
		"\n" +
		"server=/api.example.com/8.8.8.8\n" +
		"server=/example.com/8.8.8.8\n" +
		"\n" +
		"ipset=/api.example.com/enclave_allowed\n" +
		"ipset=/example.com/enclave_allowed\n"
	if got != want {
		t.Fatalf("no-deny output drifted:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderDNSMasqConfigDeniedSubdomainOfWildcard(t *testing.T) {
	// A denied subdomain of an allowed parent emits a blackhole address= line
	// (dnsmasq's most-specific match wins) and never an ipset line, while the
	// parent's allow lines stay intact.
	got := RenderDNSMasqConfig([]string{"example.com"}, []string{"tracking.example.com"}, "8.8.8.8")
	if !strings.Contains(got, "server=/example.com/8.8.8.8") {
		t.Fatalf("expected parent allow line intact, got:\n%s", got)
	}
	if !strings.Contains(got, "ipset=/example.com/enclave_allowed") {
		t.Fatalf("expected parent ipset line intact, got:\n%s", got)
	}
	if !strings.Contains(got, "address=/tracking.example.com/") {
		t.Fatalf("expected denied blackhole line, got:\n%s", got)
	}
	if strings.Contains(got, "ipset=/tracking.example.com/") {
		t.Fatalf("did not expect an ipset line for a denied domain, got:\n%s", got)
	}
	if strings.Contains(got, "server=/tracking.example.com/") {
		t.Fatalf("did not expect a server line for a denied domain, got:\n%s", got)
	}
}

func TestRenderDnsmasqConfigForToolDenyWinsOnExactMatch(t *testing.T) {
	// A domain present in both the allow set and the denied set is dropped from
	// the allow server=/ipset= lines and rendered only as a blackhole address=.
	ep := EffectivePolicy{
		Mode:          "restricted",
		Domains:       []string{"example.com", "tracking.example.com"},
		DeniedDomains: []string{"tracking.example.com"},
	}
	got, err := RenderDnsmasqConfigForTool(ep, "claude", "8.8.8.8")
	if err != nil {
		t.Fatalf("render dnsmasq: %v", err)
	}
	if strings.Contains(got, "server=/tracking.example.com/") {
		t.Fatalf("expected exact-denied domain dropped from allow lines, got:\n%s", got)
	}
	if strings.Contains(got, "ipset=/tracking.example.com/") {
		t.Fatalf("expected no ipset line for exact-denied domain, got:\n%s", got)
	}
	if !strings.Contains(got, "address=/tracking.example.com/") {
		t.Fatalf("expected blackhole line for exact-denied domain, got:\n%s", got)
	}
	if !strings.Contains(got, "server=/example.com/8.8.8.8") {
		t.Fatalf("expected surviving allow domain, got:\n%s", got)
	}
}

func TestEffectiveRenderDomainsDenyWinsOverSubdomainAllow(t *testing.T) {
	// An allow entry that is a SUBDOMAIN of a denied domain must be filtered
	// from the allow set: otherwise its more-specific server= line out-ranks
	// the parent's blackhole under dnsmasq longest-match and re-opens the deny.
	ep := EffectivePolicy{
		Mode:          "restricted",
		Domains:       []string{"example.com", "api.tracking.example.com", "other.com"},
		DeniedDomains: []string{"tracking.example.com"},
	}
	allow, denied, err := EffectiveRenderDomainsForTool(ep, "claude")
	if err != nil {
		t.Fatalf("EffectiveRenderDomainsForTool: %v", err)
	}
	for _, d := range allow {
		if d == "api.tracking.example.com" {
			t.Fatalf("subdomain of denied domain leaked into allow set: %v", allow)
		}
	}
	if len(denied) != 1 || denied[0] != "tracking.example.com" {
		t.Fatalf("unexpected denied set: %v", denied)
	}
	// A denial more-specific than an allow leaves the broader allow intact.
	if !containsStr(allow, "example.com") || !containsStr(allow, "other.com") {
		t.Fatalf("expected broader/unrelated allows to survive, got %v", allow)
	}
}

func containsStr(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestNormalizePolicyDomain(t *testing.T) {
	cases := []struct {
		in    string
		valid bool
	}{
		{"example.com", true},
		{"sub.example.com", true},
		{"*.example.com", false},
		{"*.a.b.c", false},
		{"*", false},
		{"**", false},
		{"*.", false},
		{".*", false},
		{"*.*.example.com", false},
		{"*.-bad.example.com", false},
		{"", false},
		{"evil; rm -rf /", false},
		{"newline\n.com", false},
	}
	for _, c := range cases {
		got, err := NormalizePolicyDomain(c.in)
		if valid := err == nil && got != ""; valid != c.valid {
			t.Errorf("NormalizePolicyDomain(%q) = (%q, %v), want valid=%v", c.in, got, err, c.valid)
		}
	}
}

func TestRenderDnsmasqConfigForToolRejectsUnrestricted(t *testing.T) {
	ep := EffectivePolicy{Mode: "unrestricted"}
	if _, err := RenderDnsmasqConfigForTool(ep, "claude", "8.8.8.8"); err == nil {
		t.Fatal("expected unrestricted mode error")
	}
}

func TestRenderDnsmasqConfigPreviewForToolPrintsUnrestricted(t *testing.T) {
	ep := EffectivePolicy{Mode: "unrestricted"}
	got, err := RenderDnsmasqConfigPreviewForTool(ep, "claude", "8.8.8.8")
	if err != nil {
		t.Fatalf("preview unrestricted: %v", err)
	}
	if got != "# Mode: unrestricted\n" {
		t.Fatalf("unexpected unrestricted preview: %q", got)
	}
}

func TestCanonicalPolicyDomain(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty", input: "   ", want: ""},
		{name: "normalize", input: ".Example.com", want: "example.com"},
		{name: "reject slash", input: "evil.com/8.8.8.8", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalPolicyDomain(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("CanonicalPolicyDomain(%q) error = nil, want non-nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("CanonicalPolicyDomain(%q) error = %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("CanonicalPolicyDomain(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEffectiveDomainsForToolDeduplicates(t *testing.T) {
	got, err := EffectiveDomainsForTool(EffectivePolicy{
		Domains: []string{"Example.com", ".example.com"},
		ToolDomains: map[string][]string{
			"codex": {"api.example.com", "api.example.com"},
		},
	}, "codex")
	if err != nil {
		t.Fatalf("EffectiveDomainsForTool() error = %v", err)
	}
	want := []string{"api.example.com", "example.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("EffectiveDomainsForTool() = %v, want %v", got, want)
	}
}
