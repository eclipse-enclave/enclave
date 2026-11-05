// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"enclave/internal/a2a"
	"enclave/internal/peergroup"
)

// rulesSnapshot publishes the interfaces.md example group: an orchestrator
// and two workers, one of them not attached to the hub yet.
func rulesSnapshot(t *testing.T) (string, peergroup.Revision) {
	t.Helper()
	root := t.TempDir()
	pub := peergroup.NewSnapshotPublisher(root, nil)
	member := func(name string, role a2a.Role, gen string, gw, hub string, port int) peergroup.MembershipMember {
		return peergroup.MembershipMember{
			Name: name, Role: role, Generation: peergroup.Generation(gen), ProjectPath: "/src", MountMode: "ro",
			GatewayAddress: gw, HubAddress: hub, AdapterPort: port, InboundTokenHash: hashToken(name),
		}
	}
	m := peergroup.Membership{
		Group: testGroup, GroupGeneration: testGroupGen,
		Members: []peergroup.MembershipMember{
			member("orch", a2a.RoleOrchestrator, "1111111111111111", "172.18.0.2", "172.18.0.14", 0),
			member("worker-1", a2a.RoleWorker, "a1b2c3d4e5f60718", "172.18.0.18", "172.18.0.30", 41821),
			member("worker-2", a2a.RoleWorker, "2222222222222222", "172.18.0.34", "172.18.0.46", 41821),
		},
		HostPrincipal: peergroup.HostPrincipalEntry{Name: peergroup.HostMemberName, Generation: testHostGen},
	}
	s := peergroup.Secrets{Group: testGroup, GroupGeneration: testGroupGen}
	rev, err := pub.Publish(context.Background(), testGroup, m, s)
	if err != nil {
		t.Fatal(err)
	}
	return peergroup.SnapshotsDir(root, testGroup), rev
}

func fakeInterfaces(t *testing.T) InterfaceResolver {
	t.Helper()
	ifaces := map[string]string{"172.18.0.14": "eth0", "172.18.0.30": "eth2"}
	return func(addr netip.Addr) (string, error) {
		if name, ok := ifaces[addr.String()]; ok {
			return name, nil
		}
		return "", errors.New("no interface holds " + addr.String())
	}
}

func TestRenderRules(t *testing.T) {
	dir, rev := rulesSnapshot(t)
	rules, err := RenderRules(dir, testGroup, testGroupGen, peergroup.HubPort, fakeInterfaces(t))
	if err != nil {
		t.Fatal(err)
	}
	want := `# enclave-a2a-hub group review revision 1
*filter
:INPUT DROP [0:0]
:FORWARD DROP [0:0]
:OUTPUT DROP [0:0]
-A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
-A INPUT -i eth0 -s 172.18.0.2/32 -d 172.18.0.14/32 -p tcp --dport 41820 -j ACCEPT
-A INPUT -i eth2 -s 172.18.0.18/32 -d 172.18.0.30/32 -p tcp --dport 41820 -j ACCEPT
-A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
-A OUTPUT -o eth2 -s 172.18.0.30/32 -d 172.18.0.18/32 -p tcp --dport 41821 -j ACCEPT
COMMIT
`
	if rules.Restore != want {
		t.Fatalf("rules:\n%s\nwant:\n%s", rules.Restore, want)
	}
	if rules.Revision != rev {
		t.Fatalf("revision %d", rules.Revision)
	}
	if len(rules.Warnings) != 1 || !strings.Contains(rules.Warnings[0], "worker-2") {
		t.Fatalf("warnings %q", rules.Warnings)
	}
}

func TestRenderRulesRefusesBadSnapshots(t *testing.T) {
	dir, rev := rulesSnapshot(t)
	if r, err := RenderRules(dir, testGroup, "ffffffffffffffff", 0, fakeInterfaces(t)); err == nil || r.Revision != rev || r.Restore != "" {
		t.Fatalf("other group generation: %v, revision %d", err, r.Revision)
	}
	if _, err := RenderRules(dir, "other", testGroupGen, 0, fakeInterfaces(t)); err == nil {
		t.Fatal("other group rendered")
	}
	if _, err := RenderRules(dir, testGroup, testGroupGen, 9999, fakeInterfaces(t)); err == nil {
		t.Fatal("hub port mismatch rendered")
	}
	unsafe := func(netip.Addr) (string, error) { return "eth0 -j ACCEPT", nil }
	if _, err := RenderRules(dir, testGroup, testGroupGen, 0, unsafe); err == nil {
		t.Fatal("unsafe interface name rendered")
	}
	rewrite(t, filepath.Join(dir, strconv.FormatUint(uint64(rev), 10), peergroup.MembershipFile),
		func(s string) string { return strings.Replace(s, "172.18.0.2", "172.18.0.3", 1) })
	if _, err := RenderRules(dir, testGroup, testGroupGen, 0, fakeInterfaces(t)); err == nil || !strings.Contains(err.Error(), "hashes to") {
		t.Fatalf("tampered membership: %v", err)
	}
}

func TestRenderRulesWithoutSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := RenderRules(dir, testGroup, testGroupGen, 0, fakeInterfaces(t)); err == nil {
		t.Fatal("rendered without a snapshot")
	}
}

func TestLocalInterfaceFindsLoopback(t *testing.T) {
	name, err := LocalInterface(netip.MustParseAddr("127.0.0.1"))
	if err != nil || name == "" {
		t.Skipf("no loopback address visible: %v", err)
	}
	if _, err := LocalInterface(netip.MustParseAddr("192.0.2.254")); err == nil {
		t.Fatal("found an interface for a documentation address")
	}
}
