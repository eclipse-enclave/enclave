// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package network

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

const (
	DefaultResolver = "8.8.8.8"
	DNSMasqIPSet    = "enclave_allowed"
)

// ExtractDomainsFromContent extracts domains from dnsmasq server= lines.
func ExtractDomainsFromContent(content []byte) []string {
	var domains []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "server=/") {
			continue
		}
		// server=/domain/resolver
		rest := strings.TrimPrefix(line, "server=/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) < 1 || parts[0] == "" {
			continue
		}
		domains = append(domains, strings.ToLower(parts[0]))
	}
	return domains
}

// ResolveAllowlistIncludePath resolves a dnsmasq conf-file value within allowlistsDir.
func ResolveAllowlistIncludePath(allowlistsDir string, includePath string) (string, bool) {
	includePath = strings.TrimPrefix(includePath, model.GatewayAllowlistsDir+"/")
	fullPath := filepath.Join(allowlistsDir, includePath)
	within, err := util.RealPathWithin(allowlistsDir, fullPath)
	return fullPath, err == nil && within
}

// ExtractDomainsRecursive extracts domains from a dnsmasq allowlist file,
// following conf-file= includes relative to allowlistsDir.
func ExtractDomainsRecursive(allowlistPath string, allowlistsDir string) ([]string, error) {
	// #nosec G304 -- allowlistPath is resolved from trusted allowlist sources.
	data, err := os.ReadFile(allowlistPath)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var domains []string

	addDomains := func(content []byte) {
		for _, d := range ExtractDomainsFromContent(content) {
			if !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
	}

	addDomains(data)

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "conf-file=") {
			continue
		}
		includePath := strings.TrimPrefix(line, "conf-file=")
		fullPath, ok := ResolveAllowlistIncludePath(allowlistsDir, includePath)
		if !ok {
			continue
		}
		// #nosec G304 G703 -- fullPath is constrained to the allowlists directory above.
		includeData, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		addDomains(includeData)
	}

	sort.Strings(domains)
	return domains, nil
}

// DomainToServerLine converts a domain to a dnsmasq server= directive.
func DomainToServerLine(domain string, resolver string) string {
	if resolver == "" {
		resolver = DefaultResolver
	}
	return "server=/" + strings.ToLower(domain) + "/" + resolver
}

// DomainToIPSetLine converts a domain to a dnsmasq ipset= directive.
func DomainToIPSetLine(domain string) string {
	return "ipset=/" + strings.ToLower(domain) + "/" + DNSMasqIPSet
}

// DomainToBlackholeLine converts a domain to a dnsmasq address= directive with
// no address, which resolves the domain (and its subdomains) to 0.0.0.0. It is
// more specific than a parent's server= line, so dnsmasq's longest-match rule
// makes an explicit deny win over a broader allow. Denied domains get no ipset
// line — they never resolve to a real IP.
func DomainToBlackholeLine(domain string) string {
	return "address=/" + strings.ToLower(domain) + "/"
}

// EffectiveDomainsForTool returns the sorted, canonical domains that a gateway
// bundle for tool will enforce: global policy domains plus that tool's domains.
func EffectiveDomainsForTool(policy EffectivePolicy, tool string) ([]string, error) {
	return canonicalDedupSorted(policy.Domains, policy.ToolDomains[tool])
}

// canonicalDedupSorted canonicalizes every value across the given groups
// (dropping empties), removes duplicates, and returns the result sorted.
func canonicalDedupSorted(groups ...[]string) ([]string, error) {
	seen := make(map[string]struct{})
	var out []string
	for _, group := range groups {
		for _, value := range group {
			domain, err := CanonicalPolicyDomain(value)
			if err != nil {
				return nil, err
			}
			if domain == "" {
				continue
			}
			if _, ok := seen[domain]; ok {
				continue
			}
			seen[domain] = struct{}{}
			out = append(out, domain)
		}
	}
	sort.Strings(out)
	return out, nil
}

// EffectiveDeniedDomains returns the sorted, canonical denied domains that a
// gateway bundle will blackhole. Denied domains are policy-wide (not per-tool),
// mirroring how spec.yaml network.deniedDomains is declared.
func EffectiveDeniedDomains(policy EffectivePolicy) ([]string, error) {
	return canonicalDedupSorted(policy.DeniedDomains)
}

// EffectiveRenderDomainsForTool returns the canonical (allow, denied) domain
// lists a gateway renders for tool. Deny wins unconditionally: an allow entry
// that equals a denied domain OR is a subdomain of one is removed from the
// allow set. The subdomain filter matters because dnsmasq resolves by longest
// match — an allow line server=/api.tracker.example/ would otherwise out-rank
// the parent's address=/tracker.example/ blackhole and quietly re-open the
// denied domain. A deny that is MORE specific than an allow (allow
// example.com, deny telemetry.example.com) needs no filtering: its own
// address= line already out-ranks the broader allow.
func EffectiveRenderDomainsForTool(policy EffectivePolicy, tool string) (allow []string, denied []string, err error) {
	allow, err = EffectiveDomainsForTool(policy, tool)
	if err != nil {
		return nil, nil, err
	}
	denied, err = EffectiveDeniedDomains(policy)
	if err != nil {
		return nil, nil, err
	}
	if len(denied) == 0 {
		return allow, denied, nil
	}
	filtered := make([]string, 0, len(allow))
	for _, d := range allow {
		if !domainDeniedBy(d, denied) {
			filtered = append(filtered, d)
		}
	}
	return filtered, denied, nil
}

// domainDeniedBy reports whether domain equals any denied entry or is a
// subdomain of one ("deny rules take precedence over allow rules").
func domainDeniedBy(domain string, denied []string) bool {
	for _, d := range denied {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}

func CanonicalPolicyDomain(value string) (string, error) {
	domain := strings.TrimSpace(strings.TrimPrefix(value, "."))
	if domain == "" {
		return "", nil
	}
	normalized, err := NormalizePolicyDomain(domain)
	if err != nil {
		return "", fmt.Errorf("invalid domain %q: %w", strings.TrimSpace(value), err)
	}
	return normalized, nil
}

// RenderDNSMasqConfig renders the dnsmasq bundle format used by live gateways.
// denied domains are blackholed with address= lines after the allow block; an
// empty denied list yields output byte-identical to the allow-only rendering.
func RenderDNSMasqConfig(domains []string, denied []string, resolver string) string {
	if resolver == "" {
		resolver = DefaultResolver
	}

	var b strings.Builder
	b.WriteString("# Generated by enclave\n")
	b.WriteString("no-resolv\n")
	b.WriteString("no-poll\n")
	b.WriteString("address=/#/\n")
	if len(domains) > 0 {
		b.WriteString("\n")
	}

	for _, domain := range domains {
		b.WriteString(DomainToServerLine(domain, resolver))
		b.WriteString("\n")
	}
	if len(domains) > 0 {
		b.WriteString("\n")
	}

	for _, domain := range domains {
		b.WriteString(DomainToIPSetLine(domain))
		b.WriteString("\n")
	}

	if len(denied) > 0 {
		b.WriteString("\n")
		for _, domain := range denied {
			b.WriteString(DomainToBlackholeLine(domain))
			b.WriteString("\n")
		}
	}

	return b.String()
}

func RenderDnsmasqConfigForTool(ep EffectivePolicy, tool string, resolver string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(ep.Mode), model.NetworkModeUnrestricted) {
		return "", fmt.Errorf("live gateway apply does not support unrestricted mode")
	}
	domains, denied, err := EffectiveRenderDomainsForTool(ep, tool)
	if err != nil {
		return "", err
	}
	return RenderDNSMasqConfig(domains, denied, resolver), nil
}

func RenderDnsmasqConfigPreviewForTool(ep EffectivePolicy, tool string, resolver string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(ep.Mode), model.NetworkModeUnrestricted) {
		return "# Mode: unrestricted\n", nil
	}
	return RenderDnsmasqConfigForTool(ep, tool, resolver)
}

// FirstIPv4Resolver picks a single upstream IPv4 resolver from candidates,
// falling back to DefaultResolver. Both the deployed gateway bundle and the
// `network print` preview use this so the preview matches what is deployed.
// This is an intentional simplification: per-domain resolver mappings from
// nested allowlist fragments are not preserved in the bundle renderer.
func FirstIPv4Resolver(candidates []string) string {
	for _, candidate := range candidates {
		ip := net.ParseIP(strings.TrimSpace(candidate))
		if ip == nil {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
	}
	return DefaultResolver
}
