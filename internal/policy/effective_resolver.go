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
	ProjectDir  string
	ProjectHash string // Runtime owner, never used as a config source when ConfigHash is set.
	ConfigHash  string
	Tool        string
	// Session supplies the session's spec, feature, and ad-hoc domains. Leave
	// nil to load only the tool's spec, as for a running gateway whose session
	// options are unknown.
	Session *SessionNetworkInputs
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

	configHash := strings.TrimSpace(input.ConfigHash)
	if configHash == "" {
		configHash = projectHash
	}
	policyConfigHash := ""
	if projectDir != "" {
		policyConfigHash = configHash
	}
	globalPolicy, projectPolicy, err := loadPolicies(r.home, policyConfigHash)
	if err != nil {
		return empty, err
	}

	allowlistPath := network.ResolveToolAllowlist(r.paths, tool)
	allowlistPath = config.ResolveAllowlistPath(tool, r.home, configHash, allowlistPath)

	session := input.Session
	if session == nil {
		loaded, err := r.loadSpecSession(tool)
		if err != nil {
			return empty, err
		}
		session = &loaded
	}

	effective := session.Merge(network.MergeConfig{
		BuiltInAllowlistPath: allowlistPath,
		AllowlistsDir:        r.paths.AllowlistsDir,
		GlobalPolicy:         globalPolicy,
		ProjectPolicy:        projectPolicy,
	})
	return ResolveResult{
		AllowlistPath: allowlistPath,
		GlobalPolicy:  globalPolicy,
		ProjectPolicy: projectPolicy,
		Effective:     effective,
	}, nil
}

// NetworkMode returns the network mode the global and project policy files
// set, before --allow-all-network applies. An empty projectHash ignores the
// project policy, as Resolve does for a session outside a project.
func NetworkMode(home string, projectHash string) (string, error) {
	globalPolicy, projectPolicy, err := loadPolicies(home, projectHash)
	if err != nil {
		return "", err
	}
	return network.Merge(network.MergeConfig{GlobalPolicy: globalPolicy, ProjectPolicy: projectPolicy}).Mode, nil
}

// loadPolicies loads the global policy and, when projectHash is set, the
// project policy.
func loadPolicies(home string, projectHash string) (globalPolicy network.Policy, projectPolicy network.Policy, err error) {
	globalPath := network.GlobalPolicyPath(home)
	globalPolicy, err = network.LoadPolicy(globalPath)
	if err != nil {
		return network.Policy{}, network.Policy{}, fmt.Errorf("load global policy %s: %w", globalPath, err)
	}
	if projectHash == "" {
		return globalPolicy, network.Policy{}, nil
	}
	projectPath := network.ProjectPolicyPath(home, projectHash)
	projectPolicy, err = network.LoadPolicy(projectPath)
	if err != nil {
		return network.Policy{}, network.Policy{}, fmt.Errorf("load project policy %s: %w", projectPath, err)
	}
	return globalPolicy, projectPolicy, nil
}

// loadSpecSession returns session inputs carrying only the tool's spec. A
// tool without a spec has no inline domains and is not an error; a malformed
// spec is surfaced so live resolution fails as session startup would.
func (r EffectiveResolver) loadSpecSession(tool string) (SessionNetworkInputs, error) {
	profile, err := config.LoadProfile(r.paths, tool)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SessionNetworkInputs{}, nil
		}
		return SessionNetworkInputs{}, fmt.Errorf("load spec domains for %q: %w", tool, err)
	}
	return SessionNetworkInputs{Profile: profile}, nil
}
