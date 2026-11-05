// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux

package main

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/peergroup"
)

// TestProxyReloadsOnSIGHUP runs the proxy subcommand in-process and drives it
// with real signals, as the supervisor does.
func TestProxyReloadsOnSIGHUP(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	root := t.TempDir()
	statusDir := peergroup.StatusDir(root, "review")
	auditDir := peergroup.AuditDir(root, "review")
	for _, d := range []string{statusDir, auditDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pub := peergroup.NewSnapshotPublisher(root, nil)
	publishRev := func(orch string) {
		m := peergroup.Membership{
			Group: "review", GroupGeneration: testGen, HubPort: port,
			Members: []peergroup.MembershipMember{{
				Name: orch, Role: a2a.RoleOrchestrator, Generation: "1111111111111111", MountMode: "rw",
				GatewayAddress: "127.0.0.1", HubAddress: "127.0.0.1", InboundTokenHash: "sha256:" + strings.Repeat("ab", 32),
			}},
			HostPrincipal: peergroup.HostPrincipalEntry{Name: peergroup.HostMemberName, Generation: "0011223344556677"},
		}
		if _, err := pub.Publish(context.Background(), "review", m, peergroup.Secrets{Group: "review", GroupGeneration: testGen}); err != nil {
			t.Fatal(err)
		}
	}
	publishRev("orch")

	vars := map[string]string{
		a2a.EnvGroup: "review", a2a.EnvGroupGeneration: testGen, a2a.EnvHubPort: strconv.Itoa(port),
		a2a.EnvHubEpoch: "abcdef0123456789", a2a.EnvHubSnapshotDir: peergroup.SnapshotsDir(root, "review"),
		a2a.EnvHubStatusDir: statusDir, a2a.EnvHubAuditDir: auditDir,
	}
	done := make(chan int, 1)
	var stderr strings.Builder
	go func() { done <- run([]string{"proxy"}, nil, &strings.Builder{}, &stderr, env(vars)) }()

	waiter := peergroup.NewStatusWaiter(root)
	wait := func(rev peergroup.Revision) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, s, err := waiter.Read("review"); err == nil && s != nil && s.AppliedRevision == rev {
				if s.Epoch != "abcdef0123456789" {
					t.Fatalf("epoch %s", s.Epoch)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("revision %d not applied", rev)
	}
	wait(1)
	publishRev("orch-2")
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	wait(2)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("proxy did not stop on SIGTERM")
	}
}
