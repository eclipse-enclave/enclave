// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package network

import (
	"path/filepath"
	"sort"
	"strings"

	"enclave/internal/model"
	"enclave/internal/util"
)

// EffectivePolicy is the resolved policy after merging all sources.
type EffectivePolicy struct {
	Mode                 string
	ModeSource           string
	InheritToolAllowlist bool
	Domains              []string
	ToolDomains          map[string][]string
	// DeniedDomains lists domains that must be blackholed even when a broader
	// allow would otherwise resolve them (deny wins via most-specific match).
	// They are kept out of Domains and never resolve to a real IP.
	DeniedDomains []string
	Resolvers     []string
	Sources       []PolicySource
}

// PolicySource identifies a policy file that contributed to the effective policy.
type PolicySource struct {
	Name string
	Path string
}

// MergeConfig holds all inputs for policy merge.
type MergeConfig struct {
	BuiltInAllowlistPath string
	AllowlistsDir        string
	GlobalPolicy         Policy
	ProjectPolicy        Policy
	// SpecAllowedDomains carries a spec.yaml extension's network.allowedDomains.
	// It is additive to the built-in allowlist and policy-derived domains, not
	// a replacement.
	SpecAllowedDomains []string
	// SpecDeniedDomains carries a spec.yaml extension's network.deniedDomains.
	// These are collected into EffectivePolicy.DeniedDomains (never the allow
	// set) so the renderer can blackhole them; deny wins over any broader allow.
	SpecDeniedDomains []string
}

// Merge resolves an EffectivePolicy from all sources.
func Merge(cfg MergeConfig) EffectivePolicy {
	ep := EffectivePolicy{
		Mode:                 model.NetworkModeRestricted,
		ModeSource:           "default",
		InheritToolAllowlist: true,
		ToolDomains:          make(map[string][]string),
	}

	ep.resolveModeAndFlags(cfg)

	// Collect domains from all sources (union, deduplicated)
	seen := make(map[string]bool)
	addDomain := func(d string) {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && !seen[d] {
			seen[d] = true
			ep.Domains = append(ep.Domains, d)
		}
	}

	ep.collectBuiltInDomains(cfg, addDomain)
	ep.collectPolicyDomains(cfg, addDomain)
	ep.collectSpecDomains(cfg, addDomain)
	ep.collectSpecDeniedDomains(cfg)

	sort.Strings(ep.Domains)
	sort.Strings(ep.DeniedDomains)
	return ep
}

// resolveModeAndFlags applies mode, inherit-tool-allowlist, and resolver
// precedence (project > global > default) onto ep.
func (ep *EffectivePolicy) resolveModeAndFlags(cfg MergeConfig) {
	// Resolve mode: project > global > default
	if cfg.GlobalPolicy.Mode != "" {
		ep.Mode = cfg.GlobalPolicy.Mode
		ep.ModeSource = "global"
	}
	if cfg.ProjectPolicy.Mode != "" {
		ep.Mode = cfg.ProjectPolicy.Mode
		ep.ModeSource = "project"
	}

	// Resolve inherit_tool_allowlist: project > global > true
	if cfg.GlobalPolicy.InheritToolAllowlist != nil {
		ep.InheritToolAllowlist = *cfg.GlobalPolicy.InheritToolAllowlist
	}
	if cfg.ProjectPolicy.InheritToolAllowlist != nil {
		ep.InheritToolAllowlist = *cfg.ProjectPolicy.InheritToolAllowlist
	}

	// Resolve advanced settings
	ep.Resolvers = append(ep.Resolvers, cfg.GlobalPolicy.Advanced.Resolvers...)
	ep.Resolvers = append(ep.Resolvers, cfg.ProjectPolicy.Advanced.Resolvers...)
}

// collectBuiltInDomains adds the built-in tool allowlist when tool-allowlist
// inheritance is enabled.
func (ep *EffectivePolicy) collectBuiltInDomains(cfg MergeConfig, addDomain func(string)) {
	if cfg.BuiltInAllowlistPath != "" && cfg.AllowlistsDir != "" && ep.InheritToolAllowlist {
		if builtIn, err := ExtractDomainsRecursive(cfg.BuiltInAllowlistPath, cfg.AllowlistsDir); err == nil {
			for _, d := range builtIn {
				addDomain(d)
			}
			ep.Sources = append(ep.Sources, PolicySource{Name: "built-in allowlist", Path: cfg.BuiltInAllowlistPath})
		}
	}
}

// collectPolicyDomains adds domains from the structured global and project
// policies. The project inherits global domains unless inherit_global_policy is
// explicitly false.
func (ep *EffectivePolicy) collectPolicyDomains(cfg MergeConfig, addDomain func(string)) {
	// Global policy domains
	for _, d := range cfg.GlobalPolicy.Domains.Global {
		addDomain(d)
	}
	if len(cfg.GlobalPolicy.Domains.Global) > 0 {
		ep.Sources = append(ep.Sources, PolicySource{Name: "global policy"})
	}
	for tool, domains := range cfg.GlobalPolicy.Domains.Tools {
		addToolDomains(ep.ToolDomains, tool, domains)
	}

	// Project policy domains
	inheritGlobal := true
	if cfg.ProjectPolicy.InheritGlobalPolicy != nil {
		inheritGlobal = *cfg.ProjectPolicy.InheritGlobalPolicy
	}
	if inheritGlobal {
		for _, d := range cfg.ProjectPolicy.Domains.Global {
			addDomain(d)
		}
	}
	if len(cfg.ProjectPolicy.Domains.Global) > 0 {
		ep.Sources = append(ep.Sources, PolicySource{Name: "project policy"})
	}
	for tool, domains := range cfg.ProjectPolicy.Domains.Tools {
		addToolDomains(ep.ToolDomains, tool, domains)
	}
}

// collectSpecDomains adds domains declared by a spec.yaml extension's
// network.allowedDomains. This is additive to the built-in allowlist and
// structured policy domains, never a replacement.
func (ep *EffectivePolicy) collectSpecDomains(cfg MergeConfig, addDomain func(string)) {
	if len(cfg.SpecAllowedDomains) == 0 {
		return
	}
	for _, d := range cfg.SpecAllowedDomains {
		addDomain(normalizeSpecDomain(d))
	}
	ep.Sources = append(ep.Sources, PolicySource{Name: "spec allowedDomains"})
}

// collectSpecDeniedDomains gathers a spec.yaml extension's network.deniedDomains
// into ep.DeniedDomains (lowercased + deduped, like the allow set) — never into
// ep.Domains. The renderer blackholes them so deny wins over any broader allow.
func (ep *EffectivePolicy) collectSpecDeniedDomains(cfg MergeConfig) {
	if len(cfg.SpecDeniedDomains) == 0 {
		return
	}
	seen := make(map[string]bool)
	for _, d := range cfg.SpecDeniedDomains {
		d = normalizeSpecDomain(strings.ToLower(strings.TrimSpace(d)))
		if d != "" && !seen[d] {
			seen[d] = true
			ep.DeniedDomains = append(ep.DeniedDomains, d)
		}
	}
	if len(ep.DeniedDomains) > 0 {
		ep.Sources = append(ep.Sources, PolicySource{Name: "spec deniedDomains"})
	}
}

// normalizeSpecDomain maps the domain forms the sbx kit format allows
// (wildcards like "*.cdn.example.com" and ports like "ampcode.com:443") onto
// the host-only form the policy layer enforces.
// dnsmasq server=/x.y/ lines already cover the apex plus all subdomains, so a
// wildcard collapses to its parent domain; ports are DNS-invisible and are
// stripped. Without this, a spec-compliant sbx kit would abort session start
// at render time ("wildcard domains are not supported").
func normalizeSpecDomain(d string) string {
	d = strings.TrimSpace(d)
	d = strings.TrimPrefix(d, "*.")
	if host, port, ok := strings.Cut(d, ":"); ok && port != "" && strings.IndexFunc(port, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		d = host
	}
	return d
}

func addToolDomains(toolDomains map[string][]string, tool string, domains []string) {
	seen := make(map[string]bool)
	for _, d := range toolDomains[tool] {
		seen[strings.ToLower(d)] = true
	}
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && !seen[d] {
			seen[d] = true
			toolDomains[tool] = append(toolDomains[tool], d)
		}
	}
}

// ResolveToolAllowlist returns the allowlist file for the given tool,
// falling back to base.conf when the tool-specific allowlist is absent.
func ResolveToolAllowlist(toolsDir string, allowlistsDir string, tool string) string {
	if tool == "" {
		return ""
	}
	allowlist := filepath.Join(toolsDir, tool, model.GatewayAllowlistFilename)
	if util.PathExists(allowlist) {
		return allowlist
	}
	fallback := filepath.Join(allowlistsDir, "base.conf")
	if util.PathExists(fallback) {
		return fallback
	}
	return ""
}
