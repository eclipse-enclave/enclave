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
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/peergroup"
)

// revokedMemory bounds how many revoked hashes and removed members the proxy
// remembers to answer "revoked" instead of "unauthenticated" or "unknown".
const revokedMemory = 4096

// revocationWait bounds how long a reload waits for revoked streams to end
// before it acknowledges the revision.
const revocationWait = 2 * time.Second

var (
	errCallerRevoked = errors.New("caller principal revoked")
	errTargetRevoked = errors.New("target member removed")
	errExpired       = errors.New("host credential expired")
	errShutdown      = errors.New("hub shutting down")
)

// Proxy is the unprivileged hub proxy.
type Proxy struct {
	cfg    Config
	epoch  string
	logger *log.Logger
	audit  *auditLog
	cache  *ownerCache
	live   *liveness
	client *http.Client
	tr     *http.Transport

	reloadMu sync.Mutex

	mu       sync.RWMutex
	st       *state
	rejected peergroup.Revision
	lastErr  string
	revoked  *memory
	removed  *memory
	conns    map[*conn]struct{}
	// revisionSubs are control streams waiting for the next applied revision.
	revisionSubs map[chan struct{}]struct{}
}

// conn is one in-flight request or stream, closed on revocation.
type conn struct {
	hash      string
	principal a2a.Principal
	target    *member
	cancel    context.CancelCauseFunc
	done      chan struct{}
}

// New creates a proxy with a fresh epoch. Call Reload before serving.
func New(cfg Config, logger *log.Logger) (*Proxy, error) {
	cfg.withDefaults()
	if !cfg.GroupGeneration.Valid() {
		return nil, fmt.Errorf("invalid group generation %q", cfg.GroupGeneration)
	}
	epoch := peergroup.Generation(cfg.Epoch)
	if epoch == "" {
		var err error
		if epoch, err = peergroup.NewGeneration(); err != nil {
			return nil, err
		}
	} else if !epoch.Valid() {
		return nil, fmt.Errorf("invalid hub epoch %q", epoch)
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         newDialer(cfg.DialTimeout, cfg.UserTimeout).DialContext,
		DisableCompression:  true,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Proxy{
		cfg:     cfg,
		epoch:   string(epoch),
		logger:  logger,
		audit:   newAuditLog(cfg.AuditDir),
		cache:   newOwnerCache(cfg.RetainedResults),
		live:    newLiveness(cfg.Now()),
		client:  &http.Client{Transport: tr},
		tr:      tr,
		revoked: newMemory(revokedMemory),
		removed: newMemory(revokedMemory),
		conns:   map[*conn]struct{}{},

		revisionSubs: map[chan struct{}]struct{}{},
	}, nil
}

// Epoch is the hub execution epoch of this process.
func (p *Proxy) Epoch() string { return p.epoch }

// Revision is the applied revision, 0 before the first.
func (p *Proxy) Revision() peergroup.Revision {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.st == nil {
		return 0
	}
	return p.st.revision
}

// Reload loads current; on success it switches routing, closes streams of
// revoked principals and removed members, then writes the status file. A
// refused revision keeps the last one and is reported in the status file
// until current loads cleanly again.
func (p *Proxy) Reload() error {
	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()

	rev := currentRevision(p.cfg.SnapshotDir)
	next, err := loadSnapshot(p.cfg.SnapshotDir, p.cfg.Group, p.cfg.GroupGeneration, true)
	if err == nil && next.hubPort != p.cfg.Port {
		err = fmt.Errorf("snapshot %d: hub port %d, proxy listens on %d", next.revision, next.hubPort, p.cfg.Port)
	}
	applied := p.Revision()
	if err == nil && next.revision < applied {
		err = fmt.Errorf("snapshot %d is older than applied revision %d", next.revision, applied)
	}
	if err != nil {
		p.mu.Lock()
		if rev > applied {
			p.rejected = rev
		}
		p.lastErr = err.Error()
		p.mu.Unlock()
		p.logger.Printf("snapshot refused, keeping revision %d: %v", applied, err)
		return errors.Join(err, p.writeStatus())
	}
	if next.revision == applied {
		// current is valid again (the refused revision was withdrawn), as the supervisor reports it.
		p.mu.Lock()
		p.rejected = 0
		p.lastErr = ""
		p.mu.Unlock()
		return p.writeStatus()
	}

	p.mu.Lock()
	old := p.st
	p.st = next
	p.rejected = 0
	p.lastErr = ""
	if old != nil {
		for _, c := range old.credentials {
			if !next.hasCredential(c.hash, c.principal) {
				p.revoked.add(c.hash, c.principal.Member)
			}
		}
		for name, m := range old.members {
			if !next.sameTarget(m) {
				p.removed.add(name, string(m.Generation))
			}
		}
	}
	for ch := range p.revisionSubs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	var closing []*conn
	for c := range p.conns {
		switch {
		case !next.hasCredential(c.hash, c.principal):
			c.cancel(errCallerRevoked)
			closing = append(closing, c)
		case c.target != nil && !next.sameTarget(c.target):
			c.cancel(errTargetRevoked)
			closing = append(closing, c)
		}
	}
	p.mu.Unlock()

	p.cache.prune(next)
	p.tr.CloseIdleConnections()
	waitClosed(closing, revocationWait)
	p.logger.Printf("applied snapshot revision %d (%d members, closed %d streams)", next.revision, len(next.members), len(closing))
	return p.writeStatus()
}

// current is the applied revision's state, nil before the first.
func (p *Proxy) current() *state {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.st
}

// subscribeRevisions signals the returned channel after every applied revision.
func (p *Proxy) subscribeRevisions() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	p.mu.Lock()
	p.revisionSubs[ch] = struct{}{}
	p.mu.Unlock()
	return ch, func() {
		p.mu.Lock()
		delete(p.revisionSubs, ch)
		p.mu.Unlock()
	}
}

func waitClosed(conns []*conn, bound time.Duration) {
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	for _, c := range conns {
		select {
		case <-c.done:
		case <-deadline.C:
			return
		}
	}
}

func (p *Proxy) writeStatus() error {
	p.mu.RLock()
	s := peergroup.StatusFile{
		Reader:           peergroup.ReaderProxy,
		GroupGeneration:  p.cfg.GroupGeneration,
		Epoch:            p.epoch,
		RejectedRevision: p.rejected,
		Error:            p.lastErr,
		UpdatedAt:        peergroup.FormatTimestamp(p.cfg.Now()),
	}
	if p.st != nil {
		s.AppliedRevision = p.st.revision
	}
	p.mu.RUnlock()
	if err := peergroup.WriteStatus(filepath.Join(p.cfg.StatusDir, peergroup.ProxyStatus), s); err != nil {
		p.logger.Printf("write status: %v", err)
		return err
	}
	return nil
}

// Close ends every open stream and the audit log.
func (p *Proxy) Close() {
	p.mu.Lock()
	var all []*conn
	for c := range p.conns {
		c.cancel(errShutdown)
		all = append(all, c)
	}
	p.mu.Unlock()
	waitClosed(all, revocationWait)
	p.tr.CloseIdleConnections()
	p.audit.close()
}

// caller is an authenticated request, registered for revocation.
type caller struct {
	st   *state
	cred credential
	conn *conn
	ctx  context.Context
}

func (c *caller) release() {
	c.conn.cancel(nil)
	close(c.conn.done)
}

// hubError is a refusal with the HTTP status used on non-JSON-RPC paths.
type hubError struct {
	err    *a2a.Error
	status int
}

func deliveryError(code int, message, reason, member string, status int) *hubError {
	var meta map[string]string
	if member != "" {
		meta = map[string]string{"member": member}
	}
	return &hubError{err: a2a.NewError(code, message, reason, meta), status: status}
}

func errUnauthenticated() *hubError {
	return deliveryError(a2a.CodeDeliveryUnauthenticated, "unauthenticated", a2a.DeliveryUnauthenticated, "", http.StatusUnauthorized)
}

func errRevoked(member string) *hubError {
	return deliveryError(a2a.CodeDeliveryRevoked, "revoked principal", a2a.DeliveryRevoked, member, http.StatusForbidden)
}

func errForbidden(member string) *hubError {
	return deliveryError(a2a.CodeDeliveryRoleForbidden, "role forbidden", a2a.DeliveryRoleForbidden, member, http.StatusForbidden)
}

func errUnknownMember(member string) *hubError {
	return deliveryError(a2a.CodeDeliveryUnknownMember, "unknown member", a2a.DeliveryUnknownMember, member, http.StatusNotFound)
}

func errUnreachable(member string) *hubError {
	return deliveryError(a2a.CodeDeliveryAdapterUnreachable, "adapter unreachable", a2a.DeliveryAdapterUnreachable, member, http.StatusBadGateway)
}

func errTooLarge() *hubError {
	return deliveryError(a2a.CodeRequestTooLarge, "request too large", a2a.DeliveryRequestTooLarge, "", http.StatusRequestEntityTooLarge)
}

// authenticate checks the bearer token against the applied revision, checks
// the caller's role and registers the request so a reload can close it.
func (p *Proxy) authenticate(r *http.Request, roles ...a2a.Role) (*caller, *hubError) {
	token, ok := strings.CutPrefix(r.Header.Get(a2a.HeaderAuthorization), a2a.BearerPrefix)
	token = strings.TrimSpace(token)
	if !ok || token == "" {
		return nil, errUnauthenticated()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.st
	if st == nil {
		return nil, errUnauthenticated()
	}
	cred, found := st.lookup(token)
	if !found {
		if member, revoked := p.revoked.get(hashToken(token)); revoked {
			return nil, errRevoked(member)
		}
		return nil, errUnauthenticated()
	}
	if !cred.expiresAt.IsZero() && !p.cfg.Now().Before(cred.expiresAt) {
		return nil, errRevoked(cred.principal.Member)
	}
	allowed := false
	for _, role := range roles {
		allowed = allowed || cred.principal.Role == role
	}
	if !allowed {
		return nil, errForbidden(cred.principal.Member)
	}
	ctx, cancel := context.WithCancelCause(r.Context())
	if !cred.expiresAt.IsZero() {
		var stop context.CancelFunc
		// Measured on the injected clock; the context deadline itself runs on the wall clock.
		ctx, stop = context.WithTimeoutCause(ctx, cred.expiresAt.Sub(p.cfg.Now()), errExpired)
		inner := cancel
		cancel = func(cause error) { inner(cause); stop() }
	}
	c := &conn{hash: cred.hash, principal: cred.principal, cancel: cancel, done: make(chan struct{})}
	p.conns[c] = struct{}{}
	return &caller{st: st, cred: cred, conn: c, ctx: ctx}, nil
}

// setTarget resolves a worker by name and binds the request to it for revocation.
func (p *Proxy) setTarget(c *caller, name string) (*member, *hubError) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m, ok := c.st.members[name]
	if !ok {
		if _, removed := p.removed.get(name); removed {
			return nil, errRevoked(name)
		}
		return nil, errUnknownMember(name)
	}
	if m.Role != a2a.RoleWorker {
		return nil, errForbidden(name)
	}
	if p.st != c.st && !p.st.sameTarget(m) {
		return nil, errRevoked(name)
	}
	c.conn.target = m
	return m, nil
}

func (p *Proxy) unregister(c *caller) {
	p.mu.Lock()
	delete(p.conns, c.conn)
	p.mu.Unlock()
	c.release()
}

// revocationError maps a closed request context to the refusal it stands for.
func revocationError(ctx context.Context, c *caller) *hubError {
	switch context.Cause(ctx) {
	case errCallerRevoked, errExpired:
		return errRevoked(c.cred.principal.Member)
	case errTargetRevoked:
		if c.conn.target != nil {
			return errRevoked(c.conn.target.Name)
		}
	}
	return nil
}

// memory is a bounded FIFO set of keys with a note each.
type memory struct {
	limit int
	order []string
	notes map[string]string
}

func newMemory(limit int) *memory { return &memory{limit: limit, notes: map[string]string{}} }

func (m *memory) add(key, note string) {
	if _, ok := m.notes[key]; !ok {
		m.order = append(m.order, key)
	}
	m.notes[key] = note
	for len(m.order) > m.limit {
		delete(m.notes, m.order[0])
		m.order = m.order[1:]
	}
}

func (m *memory) get(key string) (string, bool) {
	note, ok := m.notes[key]
	return note, ok
}
