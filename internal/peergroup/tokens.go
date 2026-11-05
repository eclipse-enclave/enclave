// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"enclave/internal/a2a"
)

// HashPrefix prefixes every token hash: "sha256:<hex>".
const HashPrefix = "sha256:"

// MemberCredentials are the two per-member tokens issued at join (proposal 7).
type MemberCredentials struct {
	// Inbound authenticates the session to the hub; only its hash is published.
	Inbound     string
	InboundHash string
	// Outbound authenticates the hub to a worker adapter; empty for an orchestrator.
	Outbound string
}

// InvocationCredential represents the host principal for one enclave a2a invocation.
type InvocationCredential struct {
	Token     string
	Hash      string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// Tokens issues and hashes credentials. Tokens are 32 random bytes,
// base64url without padding.
type Tokens interface {
	NewMember(role a2a.Role) (MemberCredentials, error)
	NewInvocation(lifetime time.Duration) (InvocationCredential, error)
	Hash(token string) string
	// Verify compares in constant time.
	Verify(token, hash string) bool
	// LoadOutbound and SaveOutbound keep the host copy of outbound tokens
	// (OutboundTokensPath, 0600), since the published secrets.json is proxy-owned.
	LoadOutbound(group string) (*Secrets, error)
	SaveOutbound(group string, s Secrets) error
}

// NewTokens returns the crypto/rand implementation.
func NewTokens(stateRoot string) Tokens {
	return &randTokens{stateRoot: stateRoot}
}

// MaxInvocationLifetime caps a host invocation credential, whatever the
// configured lifetime.
const MaxInvocationLifetime = time.Hour

type randTokens struct {
	stateRoot string
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (t *randTokens) NewMember(role a2a.Role) (MemberCredentials, error) {
	if role != a2a.RoleOrchestrator && role != a2a.RoleWorker {
		return MemberCredentials{}, fmt.Errorf("member role %q", role)
	}
	inbound, err := newToken()
	if err != nil {
		return MemberCredentials{}, err
	}
	creds := MemberCredentials{Inbound: inbound, InboundHash: t.Hash(inbound)}
	if role == a2a.RoleWorker {
		if creds.Outbound, err = newToken(); err != nil {
			return MemberCredentials{}, err
		}
	}
	return creds, nil
}

// NewInvocation truncates both times to seconds so the token's expiry equals
// the published one.
func (t *randTokens) NewInvocation(lifetime time.Duration) (InvocationCredential, error) {
	if lifetime < time.Second || lifetime > MaxInvocationLifetime {
		return InvocationCredential{}, fmt.Errorf("invocation credential lifetime %s outside 1s..%s", lifetime, MaxInvocationLifetime)
	}
	token, err := newToken()
	if err != nil {
		return InvocationCredential{}, err
	}
	issued := time.Now().UTC().Truncate(time.Second)
	return InvocationCredential{
		Token:     token,
		Hash:      t.Hash(token),
		IssuedAt:  issued,
		ExpiresAt: issued.Add(lifetime.Truncate(time.Second)),
	}, nil
}

func (t *randTokens) Hash(token string) string {
	return hashBytes([]byte(token))
}

func (t *randTokens) Verify(token, hash string) bool {
	if token == "" || !strings.HasPrefix(hash, HashPrefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(t.Hash(token)), []byte(hash)) == 1
}

func (t *randTokens) LoadOutbound(group string) (*Secrets, error) {
	if err := ValidateGroupName(group); err != nil {
		return nil, err
	}
	var s Secrets
	if err := readJSON(OutboundTokensPath(t.stateRoot, group), &s); err != nil {
		return nil, err
	}
	if s.Group != group {
		return nil, fmt.Errorf("%s names group %q", OutboundTokensPath(t.stateRoot, group), s.Group)
	}
	return &s, nil
}

func (t *randTokens) SaveOutbound(group string, s Secrets) error {
	if err := ValidateGroupName(group); err != nil {
		return err
	}
	if s.Group != group {
		return fmt.Errorf("outbound tokens for %s carry group %q", group, s.Group)
	}
	if err := checkSchema(&s.SchemaVersion); err != nil {
		return err
	}
	if s.Outbound == nil {
		s.Outbound = []OutboundSecret{}
	}
	path := OutboundTokensPath(t.stateRoot, group)
	for _, dir := range []string{GroupsDir(t.stateRoot), GroupDir(t.stateRoot, group), filepath.Dir(path)} {
		if err := ensureDir(dir, privateDirMode); err != nil {
			return err
		}
	}
	return writeJSONAtomic(path, s, privateFileMode)
}
