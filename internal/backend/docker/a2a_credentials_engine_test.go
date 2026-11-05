// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build docker

package docker

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/peergroup"
)

func TestEngineA2AHostCredentialAndHubCall(t *testing.T) {
	e := newEngineA2APrefixed(t, "a2a-f3")
	if _, err := e.joinMember("orch", a2a.RoleOrchestrator); err != nil {
		t.Fatalf("join orch: %v", err)
	}
	stateRoot := config.HostStateRootDir(e.home)

	const rounds = 5
	var issueTotal, revokeTotal time.Duration
	for i := 0; i < rounds; i++ {
		start := time.Now()
		token, hash, err := e.p.IssueHostCredential(e.ctx, e.group(), 2*time.Minute)
		issued := time.Since(start)
		if err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
		rec, err := e.p.store.Load(e.group())
		if err != nil || rec.Pending != nil || len(rec.Credentials) != 1 || rec.Credentials[0].Hash != hash || token == "" {
			t.Fatalf("record after issue %d = %+v, %v", i, rec, err)
		}
		if got := publishedCredentials(t, stateRoot, e.group()); len(got) != 1 || got[0] != hash {
			t.Fatalf("published credentials after issue %d = %v", i, got)
		}
		secrets := filepath.Join(peergroup.SnapshotDir(stateRoot, e.group(), rec.Committed), peergroup.SecretsFile)
		info, err := os.Stat(secrets)
		if err != nil {
			t.Fatal(err)
		}
		if stat, _ := info.Sys().(*syscall.Stat_t); stat == nil || int(stat.Uid) == os.Getuid() {
			t.Fatalf("credential revision's secrets.json is still the host user's: %v", info.Sys())
		}
		start = time.Now()
		if err := e.p.RevokeHostCredential(e.ctx, e.group(), hash); err != nil {
			t.Fatalf("revoke %d: %v", i, err)
		}
		revoked := time.Since(start)
		if got := publishedCredentials(t, stateRoot, e.group()); len(got) != 0 {
			t.Fatalf("published credentials after revoke %d = %v", i, got)
		}
		t.Logf("round %d: issue %s, revoke %s", i, issued.Round(time.Millisecond), revoked.Round(time.Millisecond))
		issueTotal += issued
		revokeTotal += revoked
	}
	t.Logf("mean over %d rounds: issue %s, revoke %s", rounds, (issueTotal / rounds).Round(time.Millisecond), (revokeTotal / rounds).Round(time.Millisecond))

	if os.Getenv("ENCLAVE_TEST_HUB_IMAGE") != "" {
		t.Log("real hub image: skipping the stand-in call checks")
		return
	}
	ref := backend.HubRef{Group: e.group()}
	start := time.Now()
	stream, err := e.b.HubCall(e.ctx, ref, "/peers/w1", strings.NewReader("secret-cred\n{\"jsonrpc\":\"2.0\"}"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	reader := bufio.NewReader(stream)
	var head []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read first event: %v (%q)", err, head)
		}
		if line == "\n" {
			break
		}
		head = append(head, strings.TrimSuffix(line, "\n"))
	}
	firstEvent := time.Since(start)
	want := []string{"uid=65532", "path=/peers/w1", "cred=secret-cred", `body={"jsonrpc":"2.0"}`}
	if strings.Join(head, "|") != strings.Join(want, "|") {
		t.Fatalf("first event = %q, want %q", head, want)
	}
	rest, err := io.ReadAll(reader)
	total := time.Since(start)
	if err != nil || string(rest) != "done\n" {
		t.Fatalf("rest = %q, %v", rest, err)
	}
	// The stand-in sleeps 3 s between the events; a buffered call would deliver both at the end.
	if total-firstEvent < 2*time.Second {
		t.Fatalf("first event after %s, end after %s: output was buffered", firstEvent, total)
	}
	t.Logf("hub call: first event after %s, end after %s", firstEvent.Round(time.Millisecond), total.Round(time.Millisecond))

	failing, err := e.b.HubCall(e.ctx, ref, "/fail", strings.NewReader("c\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = failing.Close() }()
	if out, err := io.ReadAll(failing); err == nil || !strings.Contains(err.Error(), "proxy refused") {
		t.Fatalf("failed call = %q, %v", out, err)
	}

	// Close ends a call that is still streaming.
	open, err := e.b.HubCall(e.ctx, ref, "/peers/w1", strings.NewReader("c\n{}"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(open).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if err := open.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Close took %s", elapsed)
	}
}
