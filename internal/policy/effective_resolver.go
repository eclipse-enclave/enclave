// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package policy

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/network"
)

// EffectiveResolver centralizes effective network policy resolution so startup,
// status, and runtime-apply paths stay semantically aligned.
type EffectiveResolver struct {
	paths model.Paths
	home  string
}

type ResolveInput struct {
	ProjectDir      string
	ProjectHash     string
	Tool            string
	AllowAllNetwork bool
	// SpecAllowedDomains carries a spec.yaml extension's network.allowedDomains
	// through to network.Merge, additive to the built-in/policy domains. Leave
	// nil to have Resolve load them from the tool's spec; a caller that already
	// holds a loaded profile may pass them explicitly to skip the reload.
	SpecAllowedDomains []string
	// SpecDeniedDomains carries a spec.yaml extension's network.deniedDomains
	// through to network.Merge, blackholed so deny wins over a broader allow.
	// nil ⇒ Resolve loads it from the tool spec (alongside SpecAllowedDomains,
	// in a single profile load); a caller may pass it explicitly to skip that.
	SpecDeniedDomains []string
}

type ResolveResult struct {
	AllowlistPath string
	GlobalPolicy  network.Policy
	ProjectPolicy network.Policy
	Effective     network.EffectivePolicy
}

func NewEffectiveResolver(paths model.Paths, home string) EffectiveResolver {
	return EffectiveResolver{
		paths: paths,
		home:  home,
	}
}

func (r EffectiveResolver) Resolve(input ResolveInput) (ResolveResult, error) {
	var empty ResolveResult

	tool := strings.TrimSpace(input.Tool)
	if tool == "" {
		return empty, fmt.Errorf("tool is required")
	}
	projectDir := strings.TrimSpace(input.ProjectDir)
	projectHash := strings.TrimSpace(input.ProjectHash)
	if projectDir != "" && projectHash == "" {
		return empty, fmt.Errorf("project hash is required")
	}

	globalPolicyPath := network.GlobalPolicyPath(r.home)
	globalPolicy, err := network.LoadPolicy(globalPolicyPath)
	if err != nil {
		return empty, fmt.Errorf("load global policy %s: %w", globalPolicyPath, err)
	}

	projectPolicy := network.Policy{}
	if projectDir != "" {
		projectPolicyPath := network.ProjectPolicyPath(r.home, projectHash)
		projectPolicy, err = network.LoadPolicy(projectPolicyPath)
		if err != nil {
			return empty, fmt.Errorf("load project policy %s: %w", projectPolicyPath, err)
		}
	}

	allowlistPath := network.ResolveToolAllowlist(r.paths.ToolsDir, r.paths.AllowlistsDir, tool)
	allowlistPath = config.ResolveAllowlistPath(tool, r.home, projectHash, allowlistPath)

	// Load from the tool's spec when the caller left either nil (see
	// ResolveInput.SpecAllowedDomains/SpecDeniedDomains for the injection
	// contract). A single profile load serves both to avoid a double read.
	specAllowedDomains := input.SpecAllowedDomains
	specDeniedDomains := input.SpecDeniedDomains
	if specAllowedDomains == nil || specDeniedDomains == nil {
		allowed, denied, err := r.loadSpecDomains(tool)
		if err != nil {
			return empty, err
		}
		if specAllowedDomains == nil {
			specAllowedDomains = allowed
		}
		if specDeniedDomains == nil {
			specDeniedDomains = denied
		}
	}

	effective := network.Merge(network.MergeConfig{
		BuiltInAllowlistPath: allowlistPath,
		AllowlistsDir:        r.paths.AllowlistsDir,
		GlobalPolicy:         globalPolicy,
		ProjectPolicy:        projectPolicy,
		SpecAllowedDomains:   specAllowedDomains,
		SpecDeniedDomains:    specDeniedDomains,
	})
	if input.AllowAllNetwork {
		effective.Mode = model.NetworkModeUnrestricted
		effective.ModeSource = "allow-all-network"
	}

	return ResolveResult{
		AllowlistPath: allowlistPath,
		GlobalPolicy:  globalPolicy,
		ProjectPolicy: projectPolicy,
		Effective:     effective,
	}, nil
}

// loadSpecDomains returns the tool's spec.yaml network.allowedDomains (plus
// its declared HTTP release hosts, which must stay resolvable — mirroring the
// session runtime's union) and network.deniedDomains from a single profile
// load. A tool without a spec has no inline domains and is not an error; a
// malformed spec is surfaced so live resolution fails as session startup would.
func (r EffectiveResolver) loadSpecDomains(tool string) (allowed []string, denied []string, err error) {
	profile, err := config.LoadProfile(r.paths, tool)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("load spec domains for %q: %w", tool, err)
	}
	allowed = append(append([]string(nil), profile.AllowedDomains...), model.ReleaseHosts(profile.Secrets)...)
	return allowed, profile.DeniedDomains, nil
}
