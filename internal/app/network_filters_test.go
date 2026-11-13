// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/gateway/bundle"
	"enclave/internal/model"
)

// filteredGateway is the fixture's restricted gateway, labelled with the
// request filters it was started with.
func filteredGateway(f modeAwareFixture) backend.GatewayInfo {
	target := f.restricted
	target.Filters = []string{"gws"}
	target.FilterHosts = []string{"gmail.googleapis.com", "*.workspace.example.com"}
	target.FilterConfine = []string{"googleapis.com"}
	return target
}

func appliedDomains(t *testing.T, f modeAwareFixture, target backend.GatewayInfo) []string {
	t.Helper()
	dir := config.HostProjectGatewayConfigDir(f.resolverCtx.Home, target.ProjectHash, target.Tool)
	domains, err := bundle.ReadAllowedDomains(filepath.Join(dir, filepath.Base(model.GatewayConfigDomainsPath)))
	if err != nil {
		t.Fatal(err)
	}
	return domains
}

// A policy apply rewrites the allowlist from the policy files and the tool's
// spec. The hosts a running gateway's filters govern come from its labels and
// stay allowlisted, so a mutation does not block the filter.
func TestNetworkApplyKeepsFilterHosts(t *testing.T) {
	f := newModeAwareFixture(t)
	target := filteredGateway(f)
	manager := &fakeGatewayManager{}
	var out strings.Builder

	if code := applyPolicyToTargets(&out, manager, f.resolverCtx, []backend.GatewayInfo{target}, gatewayScopeProjectTool); code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out.String())
	}
	domains := appliedDomains(t, f, target)
	for _, want := range []string{"tool.example.com", "gmail.googleapis.com", "workspace.example.com"} {
		if !slices.Contains(domains, want) {
			t.Errorf("applied allowlist %v lacks %s", domains, want)
		}
	}

	// The confined domain stays blackholed, recorded in the labels as well.
	dir := config.HostProjectGatewayConfigDir(f.resolverCtx.Home, target.ProjectHash, target.Tool)
	conf, err := os.ReadFile(filepath.Join(dir, filepath.Base(model.GatewayConfigDNSMasqPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(conf), "address=/googleapis.com/\n") || !strings.Contains(string(conf), "server=/gmail.googleapis.com/") {
		t.Fatalf("applied dnsmasq.conf does not confine googleapis.com:\n%s", conf)
	}

	out.Reset()
	printGatewayRuntimeStates(&out, manager, f.resolverCtx, []backend.GatewayInfo{target})
	if line := targetLine(t, out.String(), target.Name); !strings.Contains(line, "IN-SYNC") {
		t.Fatalf("filtered gateway line = %q, want IN-SYNC", line)
	}

	// A gateway without filters gets no filter host, also from the same
	// bundle directory.
	if code := applyPolicyToTargets(&out, manager, f.resolverCtx, []backend.GatewayInfo{f.restricted}, gatewayScopeProjectTool); code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out.String())
	}
	if domains := appliedDomains(t, f, f.restricted); slices.Contains(domains, "gmail.googleapis.com") {
		t.Fatalf("an unfiltered gateway got the filter hosts: %v", domains)
	}
}

// Filters need restricted mode: an apply of an unrestricted policy fails for
// a filtered gateway with the reason, and set-mode refuses before it saves.
func TestUnrestrictedModeRefusedForFilteredGateways(t *testing.T) {
	f := newModeAwareFixture(t)
	f.setGlobalMode(t, model.NetworkModeUnrestricted)
	target := filteredGateway(f)
	target.SessionContainer = "speccy-session"
	manager := &fakeGatewayManager{}
	var out strings.Builder

	if code := applyPolicyToTargets(&out, manager, f.resolverCtx, []backend.GatewayInfo{target}, gatewayScopeProject); code == 0 {
		t.Fatalf("apply of an unrestricted policy to a filtered gateway succeeded\n%s", out.String())
	}
	if line := targetLine(t, out.String(), target.Name); !strings.Contains(line, "session speccy-session runs request filters gws, which need restricted mode") {
		t.Fatalf("filtered gateway line = %q", line)
	}
	if len(manager.reloaded) != 0 {
		t.Fatalf("reloaded %v", manager.reloaded)
	}

	err := checkNoFilteredGateways([]backend.GatewayInfo{f.restricted, f.unrestricted, target})
	if err == nil || !strings.Contains(err.Error(), "session speccy-session runs request filters gws, which need restricted mode. Stop that session first, or pass --no-apply") {
		t.Fatalf("checkNoFilteredGateways = %v", err)
	}
	if err := checkNoFilteredGateways(f.targets()); err != nil {
		t.Fatalf("checkNoFilteredGateways without filters = %v", err)
	}
}
