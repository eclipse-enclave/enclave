// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
)

func TestMemberCredentials(t *testing.T) {
	tok := NewTokens(t.TempDir())
	w, err := tok.NewMember(a2a.RoleWorker)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{w.Inbound, w.Outbound} {
		raw, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil || len(raw) != 32 {
			t.Fatalf("token %q: %d bytes, %v", s, len(raw), err)
		}
	}
	if w.Inbound == w.Outbound {
		t.Fatal("inbound and outbound tokens are equal")
	}
	if !strings.HasPrefix(w.InboundHash, HashPrefix) || len(w.InboundHash) != len(HashPrefix)+64 || strings.ToLower(w.InboundHash) != w.InboundHash {
		t.Fatalf("hash %q", w.InboundHash)
	}
	if !tok.Verify(w.Inbound, w.InboundHash) || tok.Verify(w.Outbound, w.InboundHash) || tok.Verify("", w.InboundHash) || tok.Verify(w.Inbound, "") {
		t.Fatal("Verify misjudged a token")
	}
	o, err := tok.NewMember(a2a.RoleOrchestrator)
	if err != nil || o.Outbound != "" || o.Inbound == "" {
		t.Fatalf("orchestrator credentials = %+v, %v", o, err)
	}
	if _, err := tok.NewMember(a2a.RoleHost); err == nil {
		t.Fatal("NewMember issued member tokens to the host role")
	}
	// Hash is of the token string, so it matches sha256 of the base64 text.
	if tok.Hash("abc") != "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("Hash(abc) = %s", tok.Hash("abc"))
	}
}

func TestInvocationCredentials(t *testing.T) {
	tok := NewTokens(t.TempDir())
	for _, d := range []time.Duration{0, -time.Second, 500 * time.Millisecond, MaxInvocationLifetime + time.Second} {
		if _, err := tok.NewInvocation(d); err == nil {
			t.Errorf("NewInvocation(%s) accepted", d)
		}
	}
	c, err := tok.NewInvocation(15 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if c.ExpiresAt.Sub(c.IssuedAt) != 15*time.Minute || c.IssuedAt.Nanosecond() != 0 || !tok.Verify(c.Token, c.Hash) {
		t.Fatalf("credential = %+v", c)
	}
	c2, _ := tok.NewInvocation(time.Minute)
	if c2.Token == c.Token {
		t.Fatal("two invocations share a token")
	}

	rec := &GroupRecord{}
	rec.AddCredential(c)
	rec.AddCredential(c2)
	if strings.Contains(rec.Credentials[0].Hash, c.Token) || rec.Credentials[0].Hash != c.Hash {
		t.Fatal("record stores more than the hash")
	}
	if got := rec.CredentialHashes(c.IssuedAt); len(got) != 2 || got[0].ExpiresAt != FormatTimestamp(c.ExpiresAt) {
		t.Fatalf("CredentialHashes = %+v", got)
	}
	expired := rec.ExpireCredentials(c.IssuedAt.Add(2 * time.Minute))
	if len(expired) != 1 || expired[0].Hash != c2.Hash || len(rec.Credentials) != 1 {
		t.Fatalf("ExpireCredentials = %+v, left %+v", expired, rec.Credentials)
	}
	if got := rec.CredentialHashes(c.ExpiresAt); len(got) != 0 {
		t.Fatalf("credential live at its expiry: %+v", got)
	}
	if !rec.RevokeCredential(c.Hash) || rec.RevokeCredential(c.Hash) || len(rec.Credentials) != 0 {
		t.Fatal("RevokeCredential")
	}
}

func TestOutboundTokens(t *testing.T) {
	state := t.TempDir()
	tok := NewTokens(state)
	if _, err := tok.LoadOutbound("review"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("LoadOutbound before save = %v", err)
	}
	s := testSecrets(testGen)
	if err := tok.SaveOutbound("review", s); err != nil {
		t.Fatal(err)
	}
	got, err := tok.LoadOutbound("review")
	if err != nil || got.Outbound[0].Token != "secret-token" || got.SchemaVersion != SchemaVersion {
		t.Fatalf("LoadOutbound = %+v, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(OutboundTokensPath(state, "review"))
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("outbound.json mode = %v", info.Mode().Perm())
		}
	}
	if err := tok.SaveOutbound("other", s); err == nil {
		t.Fatal("SaveOutbound accepted another group's secrets")
	}
}
