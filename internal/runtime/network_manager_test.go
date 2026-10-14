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

	"enclave/internal/backend"
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

func TestPartitionAdHocDomainsSeparatesDenied(t *testing.T) {
	kept, denied := partitionAdHocDomains([]string{"ok.example.com", "Blocked.example.com", "api.tracker.example"}, []string{"blocked.example.com", "tracker.example"})
	if !reflect.DeepEqual(kept, []string{"ok.example.com"}) || !reflect.DeepEqual(denied, []string{"Blocked.example.com", "api.tracker.example"}) {
		t.Fatalf("kept = %v, denied = %v", kept, denied)
	}
}

func TestNetworkPreparationSeparatesMediationFromDestinationPolicy(t *testing.T) {
	for _, selector := range []string{"restricted", "flag", "policy"} {
		for _, protected := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/protected=%t", selector, protected), func(t *testing.T) {
				home := t.TempDir()
				policy := network.Policy{Advanced: network.PolicyAdvanced{Resolvers: []string{"9.9.9.9"}}}
				if selector == "policy" {
					policy.Mode = model.NetworkModeUnrestricted
				}
				if err := network.SavePolicy(config.HostNetworkPolicyPath(home), policy); err != nil {
					t.Fatal(err)
				}
				r := &Runtime{
					host:    model.Host{Home: home},
					handler: noopRuntimeHandler{},
					run:     model.RunOptions{AllowAllNetwork: selector == "flag"},
					auth:    model.AuthOptions{ProtectAuth: protected},
				}
				result, err := newNetworkManager(r).Prepare("session", model.AuthState{}, SecretMapping{})
				if err != nil {
					t.Fatal(err)
				}
				wantGateway := protected || selector == "restricted"
				if result.Network.UsesGateway() != wantGateway || slices.Contains(result.Env, model.EnvDNSGateway+"=1") != wantGateway {
					t.Fatalf("gateway mediation = %+v, env %v, want %t", result.Network, result.Env, wantGateway)
				}
				wantMode := backend.NetworkModeUnrestricted
				if selector == "restricted" {
					wantMode = backend.NetworkModeRestricted
				}
				if selector == "flag" && result.Network.ModeSource != "allow-all-network" {
					t.Errorf("lost per-session unrestricted override source: %q", result.Network.ModeSource)
				}
				if result.Network.Mode != wantMode {
					t.Errorf("mode = %s, want %s", result.Network.Mode, wantMode)
				}
				if wantGateway && !reflect.DeepEqual(result.Network.Egress.Resolvers, []string{"9.9.9.9"}) {
					t.Errorf("lost custom DNS resolver: %v", result.Network.Egress.Resolvers)
				}
			})
		}
	}
}

// A hand-edited policy can carry a mode the CLI never writes; it must still
// restrict destinations.
func TestNetworkPreparationRestrictsUnknownModes(t *testing.T) {
	for _, mode := range []string{"Restricted", "unrestriced", "open"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			if err := network.SavePolicy(config.HostNetworkPolicyPath(home), network.Policy{Mode: mode}); err != nil {
				t.Fatal(err)
			}
			r := &Runtime{host: model.Host{Home: home}, handler: noopRuntimeHandler{}}
			result, err := newNetworkManager(r).Prepare("session", model.AuthState{}, SecretMapping{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Network.Mode != backend.NetworkModeRestricted || !result.Network.UsesGateway() {
				t.Fatalf("mode %q gave %+v, want the restricted gateway", mode, result.Network)
			}
		})
	}
}
