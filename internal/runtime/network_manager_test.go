// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/network"
	"enclave/internal/policy"
)

// The gateway must enforce the policy resolved from policy.NewSessionNetworkInputs,
// which `enclave network print` renders for the same options.
func TestNetworkPrepareEnforcesSessionNetworkInputs(t *testing.T) {
	cfg := model.RuntimeConfig{
		Paths:   model.Paths{AllowlistsDir: t.TempDir()},
		Host:    model.Host{Home: t.TempDir()},
		Project: model.Project{Dir: "/work/project", Hash: "projhash"},
		Profile: model.Profile{
			Name:           "tool",
			AllowedDomains: []string{"tool.example.com"},
			Secrets: map[string]model.SecretConfig{
				"tool-token": secretConfig([]string{"TOOL_TOKEN"}, &model.HTTPSecretReleaseConfig{Hosts: []string{"release.example.com"}}),
			},
		},
		Run: model.RunOptions{AllowDomains: []string{"adhoc.example.com"}},
		Features: []model.Extension{{
			Name:           "svc",
			AllowedDomains: []string{"svc.example.com", "telemetry.svc.example.com"},
			DeniedDomains:  []string{"telemetry.svc.example.com"},
		}},
	}
	result, err := newNetworkManager(New(cfg)).Prepare("", model.AuthState{}, SecretMapping{})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	inputs := policy.NewSessionNetworkInputs(cfg.Profile, cfg.Features, cfg.Run)
	resolved, err := policy.NewEffectiveResolver(cfg.Paths, cfg.Host.Home).Resolve(policy.ResolveInput{
		ProjectDir:  cfg.Project.Dir,
		ProjectHash: cfg.Project.Hash,
		Tool:        cfg.Profile.Name,
		Session:     &inputs,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	allowed, denied, err := network.EffectiveRenderDomainsForTool(resolved.Effective, cfg.Profile.Name)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	egress := result.Network.Egress
	if !reflect.DeepEqual(egress.AllowedDomains, allowed) || !reflect.DeepEqual(egress.DeniedDomains, denied) {
		t.Fatalf("gateway allowed=%v denied=%v, inputs allowed=%v denied=%v", egress.AllowedDomains, egress.DeniedDomains, allowed, denied)
	}
	for _, want := range []string{"tool.example.com", "release.example.com", "svc.example.com", "adhoc.example.com"} {
		if !slices.Contains(egress.AllowedDomains, want) {
			t.Fatalf("allowed = %v, want %q", egress.AllowedDomains, want)
		}
	}
	if !reflect.DeepEqual(egress.DeniedDomains, []string{"telemetry.svc.example.com"}) {
		t.Fatalf("denied = %v, want the feature's denied domain", egress.DeniedDomains)
	}
}

func TestPolicyDomainSourcePaths(t *testing.T) {
	const (
		home        = "/home/test"
		projectDir  = "/workspace/project"
		projectHash = "projecthash"
	)

	t.Run("returns none when only built-in allowlist contributes", func(t *testing.T) {
		paths := policyDomainSourcePaths(home, projectDir, projectHash, "codex", network.Policy{}, network.Policy{})
		if len(paths) != 0 {
			t.Fatalf("paths = %v, want none", paths)
		}
	})

	t.Run("returns global path when global policy contributes tool domains", func(t *testing.T) {
		paths := policyDomainSourcePaths(home, projectDir, projectHash, "codex", network.Policy{
			Domains: network.PolicyDomains{
				Tools: map[string][]string{"codex": {"api.openai.com"}},
			},
		}, network.Policy{})
		expected := []string{config.HostNetworkPolicyPath(home)}
		if !reflect.DeepEqual(paths, expected) {
			t.Fatalf("paths = %v, want %v", paths, expected)
		}
	})

	for _, inherit := range []bool{false, true} {
		for _, projectDomains := range []bool{false, true} {
			name := fmt.Sprintf("inherit=%t/projectDomains=%t", inherit, projectDomains)
			t.Run(name, func(t *testing.T) {
				projectPolicy := network.Policy{InheritGlobalPolicy: &inherit}
				if projectDomains {
					projectPolicy.Domains.Global = []string{"project.example.com"}
				}
				paths := policyDomainSourcePaths(home, projectDir, projectHash, "codex", network.Policy{
					Domains: network.PolicyDomains{
						Global: []string{"global.example.com"},
						Tools:  map[string][]string{"codex": {"api.openai.com"}},
					},
				}, projectPolicy)
				expected := []string{}
				if inherit {
					expected = append(expected, config.HostNetworkPolicyPath(home))
				}
				if projectDomains {
					expected = append(expected, config.HostProjectNetworkPolicyPath(home, projectHash))
				}
				if !reflect.DeepEqual(paths, expected) {
					t.Fatalf("paths = %v, want %v", paths, expected)
				}
			})
		}
	}

	t.Run("returns both paths when both policies contribute", func(t *testing.T) {
		paths := policyDomainSourcePaths(home, projectDir, projectHash, "codex", network.Policy{
			Domains: network.PolicyDomains{
				Global: []string{"example.com"},
			},
		}, network.Policy{
			Domains: network.PolicyDomains{
				Tools: map[string][]string{"codex": {"api.openai.com"}},
			},
		})
		expected := []string{
			config.HostNetworkPolicyPath(home),
			config.HostProjectNetworkPolicyPath(home, projectHash),
		}
		if !reflect.DeepEqual(paths, expected) {
			t.Fatalf("paths = %v, want %v", paths, expected)
		}
	})
}
