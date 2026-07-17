// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"reflect"
	"testing"

	"enclave/internal/config"
	"enclave/internal/network"
)

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
