// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/model"
	"enclave/internal/peergroup"
)

// member is one session member of an applied revision.
type member struct {
	peergroup.MembershipMember
	gateway  netip.Addr
	hub      netip.Addr
	outbound string
}

// credential is one accepted inbound token hash and the principal it stands for.
type credential struct {
	hash      string
	principal a2a.Principal
	// expiresAt is zero for member tokens.
	expiresAt time.Time
}

// state is one verified and validated revision; it is never mutated after build.
type state struct {
	revision        peergroup.Revision
	group           string
	groupGeneration peergroup.Generation
	hubPort         int
	members         map[string]*member
	// workers is sorted by name, as listed.
	workers      []*member
	orchestrator *member
	credentials  []credential
	host         peergroup.Generation
}

// loadSnapshot loads and verifies current exactly as both hub readers must;
// withSecrets also checks that every worker has its outbound token.
func loadSnapshot(dir, group string, gen peergroup.Generation, withSecrets bool) (*state, error) {
	snap, err := peergroup.LoadCurrent(dir, gen, withSecrets)
	if err != nil {
		return nil, err
	}
	return buildState(snap, group, withSecrets)
}

// currentRevision reads the revision current names, 0 when it cannot.
func currentRevision(dir string) peergroup.Revision {
	target, err := os.Readlink(filepath.Join(dir, peergroup.CurrentLinkName))
	if err != nil {
		return 0
	}
	rev, err := strconv.ParseUint(filepath.Base(target), 10, 64)
	if err != nil {
		return 0
	}
	return peergroup.Revision(rev)
}

func buildState(snap *peergroup.Snapshot, group string, withSecrets bool) (*state, error) {
	m := snap.Membership
	rev := snap.Manifest.Revision
	fail := func(format string, args ...any) error {
		return fmt.Errorf("snapshot %d: %s", rev, fmt.Sprintf(format, args...))
	}
	if group != "" && m.Group != group {
		return nil, fail("group %q, hub serves %q", m.Group, group)
	}
	if m.HubPort < 1 || m.HubPort > 65535 {
		return nil, fail("hub port %d out of range", m.HubPort)
	}
	st := &state{
		revision:        rev,
		group:           m.Group,
		groupGeneration: m.GroupGeneration,
		hubPort:         m.HubPort,
		members:         make(map[string]*member, len(m.Members)),
	}
	hashes := map[string]bool{}
	addCredential := func(c credential) error {
		if !validHash(c.hash) {
			return fail("principal %s has malformed token hash", c.principal.Member)
		}
		if hashes[c.hash] {
			return fail("token hash of %s is not unique", c.principal.Member)
		}
		hashes[c.hash] = true
		st.credentials = append(st.credentials, c)
		return nil
	}
	for i := range m.Members {
		mm := m.Members[i]
		if mm.Name == "" || model.SanitizeSessionName(mm.Name) != mm.Name {
			return nil, fail("member name %q is not a sanitized session name", mm.Name)
		}
		if _, dup := st.members[mm.Name]; dup {
			return nil, fail("member %s listed twice", mm.Name)
		}
		if !mm.Generation.Valid() {
			return nil, fail("member %s has invalid generation %q", mm.Name, mm.Generation)
		}
		gw, err := netip.ParseAddr(mm.GatewayAddress)
		if err != nil || !gw.Is4() {
			return nil, fail("member %s gateway address %q is not IPv4", mm.Name, mm.GatewayAddress)
		}
		hub, err := netip.ParseAddr(mm.HubAddress)
		if err != nil || !hub.Is4() {
			return nil, fail("member %s hub address %q is not IPv4", mm.Name, mm.HubAddress)
		}
		mem := &member{MembershipMember: mm, gateway: gw, hub: hub}
		switch mm.Role {
		case a2a.RoleWorker:
			if mm.AdapterPort < 1 || mm.AdapterPort > 65535 {
				return nil, fail("worker %s adapter port %d out of range", mm.Name, mm.AdapterPort)
			}
			st.workers = append(st.workers, mem)
		case a2a.RoleOrchestrator:
			if mm.AdapterPort != 0 {
				return nil, fail("orchestrator %s has adapter port %d", mm.Name, mm.AdapterPort)
			}
			if st.orchestrator != nil {
				return nil, fail("two orchestrators: %s and %s", st.orchestrator.Name, mm.Name)
			}
			st.orchestrator = mem
		default:
			return nil, fail("member %s has role %q", mm.Name, mm.Role)
		}
		st.members[mm.Name] = mem
		if err := addCredential(credential{hash: mm.InboundTokenHash, principal: st.principal(mm.Name, mm.Role, mm.Generation)}); err != nil {
			return nil, err
		}
	}
	hp := m.HostPrincipal
	if hp.Name != peergroup.HostMemberName || !hp.Generation.Valid() {
		return nil, fail("host principal %q generation %q is invalid", hp.Name, hp.Generation)
	}
	st.host = hp.Generation
	for _, c := range hp.Credentials {
		exp, err := peergroup.ParseTimestamp(c.ExpiresAt)
		if err != nil {
			return nil, fail("host credential expiry %q: %v", c.ExpiresAt, err)
		}
		if err := addCredential(credential{hash: c.Hash, principal: st.principal(hp.Name, a2a.RoleHost, hp.Generation), expiresAt: exp}); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(st.workers, func(a, b *member) int { return strings.Compare(a.Name, b.Name) })
	if withSecrets {
		if err := st.attachSecrets(snap.Secrets); err != nil {
			return nil, fail("%v", err)
		}
	}
	return st, nil
}

func (st *state) attachSecrets(s *peergroup.Secrets) error {
	if s == nil {
		return errors.New("secrets not loaded")
	}
	for _, o := range s.Outbound {
		mem, ok := st.members[o.Member]
		if !ok || mem.Role != a2a.RoleWorker {
			return fmt.Errorf("outbound token for %q, which is not a worker", o.Member)
		}
		if o.Generation != mem.Generation {
			return fmt.Errorf("outbound token for %s carries generation %s, member has %s", o.Member, o.Generation, mem.Generation)
		}
		if o.Token == "" || mem.outbound != "" {
			return fmt.Errorf("outbound token for %s is empty or listed twice", o.Member)
		}
		mem.outbound = o.Token
	}
	for _, w := range st.workers {
		if w.outbound == "" {
			return fmt.Errorf("worker %s has no outbound token", w.Name)
		}
	}
	return nil
}

func (st *state) principal(name string, role a2a.Role, gen peergroup.Generation) a2a.Principal {
	return a2a.Principal{
		Group:           st.group,
		GroupGeneration: string(st.groupGeneration),
		Member:          name,
		Role:            role,
		Generation:      string(gen),
	}
}

// principals lists every member by name, then the host principal (CR-A10-4).
func (st *state) principals() []a2a.MemberPrincipal {
	out := make([]a2a.MemberPrincipal, 0, len(st.members)+1)
	for _, m := range st.members {
		out = append(out, a2a.MemberPrincipal{Member: m.Name, Role: m.Role, Generation: string(m.Generation)})
	}
	slices.SortFunc(out, func(a, b a2a.MemberPrincipal) int { return strings.Compare(a.Member, b.Member) })
	return append(out, a2a.MemberPrincipal{Member: peergroup.HostMemberName, Role: a2a.RoleHost, Generation: string(st.host)})
}

// lookup returns the credential whose hash matches token, comparing every entry in constant time.
func (st *state) lookup(token string) (credential, bool) {
	h := hashToken(token)
	var found credential
	ok := false
	for _, c := range st.credentials {
		if subtle.ConstantTimeCompare([]byte(c.hash), []byte(h)) == 1 {
			found, ok = c, true
		}
	}
	return found, ok
}

// hasCredential reports whether hash still stands for the same principal.
func (st *state) hasCredential(hash string, p a2a.Principal) bool {
	if st == nil {
		return false
	}
	for _, c := range st.credentials {
		if c.hash == hash && c.principal == p {
			return true
		}
	}
	return false
}

// sameTarget reports whether st still routes name to the same adapter with the same token.
func (st *state) sameTarget(old *member) bool {
	if st == nil || old == nil {
		return false
	}
	cur, ok := st.members[old.Name]
	return ok && cur.Generation == old.Generation && cur.Role == old.Role && cur.gateway == old.gateway &&
		cur.AdapterPort == old.AdapterPort && subtle.ConstantTimeCompare([]byte(cur.outbound), []byte(old.outbound)) == 1
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return peergroup.HashPrefix + hex.EncodeToString(sum[:])
}

func validHash(h string) bool {
	if len(h) != len(peergroup.HashPrefix)+sha256.Size*2 || h[:len(peergroup.HashPrefix)] != peergroup.HashPrefix {
		return false
	}
	for _, c := range h[len(peergroup.HashPrefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
