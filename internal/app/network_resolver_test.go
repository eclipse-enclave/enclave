// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"slices"
	"strings"
	"testing"

	"enclave/internal/model"
	"enclave/internal/network"
)

func runResolverMutation(t *testing.T, run func(*CommandInput) int, args ...string) {
	t.Helper()
	input := &CommandInput{Ctx: NewAppContext(model.Paths{}, t.TempDir())}
	input.Options.CmdArgs = args
	input.Options.NoApply = true
	captureStdout(t, func() {
		if code := run(input); code != 0 {
			t.Fatalf("exit code %d", code)
		}
	})
}

func loadGlobalPolicyForTest(t *testing.T, home string) network.Policy {
	t.Helper()
	p, err := network.LoadPolicy(network.GlobalPolicyPath(home))
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	return p
}

func TestNetworkSetResolverReplacesResolversAndKeepsPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := network.GlobalPolicyPath(home)
	seed := network.Policy{Mode: "restricted"}
	seed.Domains.Global = []string{"example.com"}
	seed.Advanced.Resolvers = []string{"9.9.9.9", "8.8.4.4"}
	if err := network.SavePolicy(path, seed); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	runResolverMutation(t, runNetworkSetResolver, "1.1.1.1")

	got := loadGlobalPolicyForTest(t, home)
	if !slices.Equal(got.Advanced.Resolvers, []string{"1.1.1.1"}) {
		t.Fatalf("resolvers = %v, want [1.1.1.1]", got.Advanced.Resolvers)
	}
	if got.Mode != "restricted" || !slices.Equal(got.Domains.Global, []string{"example.com"}) {
		t.Fatalf("other policy fields changed: %+v", got)
	}
}

func TestNetworkResetResolverClearsResolvers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	runResolverMutation(t, runNetworkSetResolver, "127.0.0.11")
	runResolverMutation(t, runNetworkResetResolver)

	got := loadGlobalPolicyForTest(t, home)
	if len(got.Advanced.Resolvers) != 0 {
		t.Fatalf("resolvers = %v, want none", got.Advanced.Resolvers)
	}
	if resolver := network.FirstIPv4Resolver(got.Advanced.Resolvers); resolver != network.DefaultResolver {
		t.Fatalf("effective resolver = %s, want default %s", resolver, network.DefaultResolver)
	}
}

func TestNetworkResetResolverReportsProjectResolver(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := NewAppContext(model.Paths{}, t.TempDir())
	project, err := ctx.Project()
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}
	projectPolicy := network.Policy{}
	projectPolicy.Advanced.Resolvers = []string{"10.0.0.53"}
	if err := network.SavePolicy(network.ProjectPolicyPath(home, project.Hash), projectPolicy); err != nil {
		t.Fatalf("seed project policy: %v", err)
	}

	input := &CommandInput{Ctx: ctx}
	input.Options.NoApply = true
	out := captureStdout(t, func() {
		if code := runNetworkResetResolver(input); code != 0 {
			t.Fatalf("exit code %d", code)
		}
	})

	if !strings.Contains(out, "still sets 10.0.0.53") {
		t.Fatalf("output does not name the project resolver:\n%s", out)
	}
}
