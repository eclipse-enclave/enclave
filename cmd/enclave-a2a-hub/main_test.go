// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"strings"
	"testing"

	"enclave/internal/a2a"
	"enclave/internal/peergroup"
)

const testGen = "3f9c0a1b2c3d4e5f"

func publish(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	m := peergroup.Membership{
		Group: "review", GroupGeneration: testGen,
		Members: []peergroup.MembershipMember{{
			Name: "orch", Role: a2a.RoleOrchestrator, Generation: "1111111111111111", MountMode: "rw",
			GatewayAddress: "127.0.0.1", HubAddress: "127.0.0.1",
			InboundTokenHash: "sha256:" + strings.Repeat("ab", 32),
		}},
		HostPrincipal: peergroup.HostPrincipalEntry{Name: peergroup.HostMemberName, Generation: "0011223344556677"},
	}
	s := peergroup.Secrets{Group: "review", GroupGeneration: testGen}
	if _, err := peergroup.NewSnapshotPublisher(root, nil).Publish(context.Background(), "review", m, s); err != nil {
		t.Fatal(err)
	}
	return peergroup.SnapshotsDir(root, "review")
}

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestRenderRulesOutput(t *testing.T) {
	dir := publish(t)
	var stdout, stderr strings.Builder
	code := run([]string{"render-rules", "--snapshot-dir", dir}, nil, &stdout, &stderr,
		env(map[string]string{a2a.EnvGroup: "review", a2a.EnvGroupGeneration: testGen}))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "# enclave-a2a-hub group review revision 1\n*filter\n") || !strings.HasSuffix(stdout.String(), "COMMIT\n") {
		t.Fatalf("stdout %q", stdout.String())
	}
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if lines[len(lines)-1] != "revision=1" {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestRenderRulesRefusal(t *testing.T) {
	dir := publish(t)
	var stdout, stderr strings.Builder
	code := run([]string{"render-rules", "--snapshot-dir", dir}, nil, &stdout, &stderr,
		env(map[string]string{a2a.EnvGroupGeneration: "ffffffffffffffff"}))
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if code == 0 || stdout.Len() != 0 || len(lines) != 2 || lines[0] != "revision=1" || !strings.Contains(lines[1], "group generation") {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"call"}, {"proxy", "extra"}, {"render-rules", "extra"}} {
		var stderr strings.Builder
		if code := run(args, nil, &strings.Builder{}, &stderr, env(nil)); code != 2 {
			t.Errorf("%q: exit %d", args, code)
		}
	}
}

func TestProxyRefusesBadEnvironment(t *testing.T) {
	var stderr strings.Builder
	if code := run([]string{"proxy"}, nil, &strings.Builder{}, &stderr, env(map[string]string{a2a.EnvGroup: "review"})); code != 1 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
}
