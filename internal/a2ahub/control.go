// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/peergroup"
)

// liveness tracks the orchestrator liveness connections (proposal 13); only
// they establish orchestrator liveness.
type liveness struct {
	mu    sync.Mutex
	count map[owner]int
	state a2a.OrchestratorState
	subs  map[chan struct{}]struct{}
}

func newLiveness(now time.Time) *liveness {
	return &liveness{
		count: map[owner]int{},
		state: a2a.OrchestratorState{Since: peergroup.FormatTimestamp(now)},
		subs:  map[chan struct{}]struct{}{},
	}
}

func (l *liveness) connect(o owner, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.count[o]++
	if l.count[o] == 1 {
		l.set(a2a.OrchestratorState{Member: o.member, Generation: o.generation, Connected: true, Since: peergroup.FormatTimestamp(now)})
	}
}

func (l *liveness) disconnect(o owner, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.count[o]--
	if l.count[o] > 0 {
		return
	}
	delete(l.count, o)
	// Another generation may still be connected (replacement orchestrator).
	for other := range l.count {
		l.set(a2a.OrchestratorState{Member: other.member, Generation: other.generation, Connected: true, Since: peergroup.FormatTimestamp(now)})
		return
	}
	l.set(a2a.OrchestratorState{Member: o.member, Generation: o.generation, Connected: false, Since: peergroup.FormatTimestamp(now)})
}

func (l *liveness) set(s a2a.OrchestratorState) {
	if s == l.state {
		return
	}
	l.state = s
	for ch := range l.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (l *liveness) current() a2a.OrchestratorState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

func (l *liveness) subscribe() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	l.mu.Lock()
	l.subs[ch] = struct{}{}
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		delete(l.subs, ch)
		l.mu.Unlock()
	}
}

// handleControl serves a worker adapter's control stream: hello, the current
// orchestrator state, every change of it, a membership event per applied
// revision, and keepalive comments.
func (p *Proxy) handleControl(w http.ResponseWriter, r *http.Request) {
	c, herr := p.authenticate(r, a2a.RoleWorker)
	if herr != nil {
		writeHubError(w, herr)
		return
	}
	defer p.unregister(c)
	changes, unsubscribe := p.live.subscribe()
	defer unsubscribe()
	// Subscribe before hello so a revision applied in between is still reported.
	revisions, unsubscribeRevisions := p.subscribeRevisions()
	defer unsubscribeRevisions()
	sse, ok := p.startStream(w, c)
	if !ok {
		return
	}
	sentRevision := c.st.revision
	sendMembership := func() bool {
		st := p.current()
		if c.ctx.Err() != nil {
			return false
		}
		if st == nil || st.revision == sentRevision {
			return true
		}
		sentRevision = st.revision
		return writeEvent(sse, a2a.ControlEventMembership, a2a.ControlMembership{Revision: uint64(st.revision), Principals: st.principals()})
	}
	sent := a2a.OrchestratorState{}
	sendState := func() bool {
		s := p.live.current()
		if s.Member == "" && c.st.orchestrator != nil {
			s.Member, s.Generation = c.st.orchestrator.Name, string(c.st.orchestrator.Generation)
		}
		if s == sent || s.Member == "" {
			return true
		}
		sent = s
		return writeEvent(sse, a2a.ControlEventOrchestrator, s)
	}
	if !sendState() || !sendMembership() {
		return
	}
	p.keepalive(c, sse, changes, sendState, revisions, sendMembership)
}

// handleLiveness serves the orchestrator client's liveness stream: hello and keepalives only.
func (p *Proxy) handleLiveness(w http.ResponseWriter, r *http.Request) {
	c, herr := p.authenticate(r, a2a.RoleOrchestrator)
	if herr != nil {
		writeHubError(w, herr)
		return
	}
	defer p.unregister(c)
	sse, ok := p.startStream(w, c)
	if !ok {
		return
	}
	o := ownerOf(c.cred.principal)
	p.live.connect(o, p.cfg.Now())
	p.logger.Printf("orchestrator %s (%s) liveness connected", o.member, o.generation)
	defer func() {
		p.live.disconnect(o, p.cfg.Now())
		p.logger.Printf("orchestrator %s (%s) liveness lost", o.member, o.generation)
	}()
	p.keepalive(c, sse, nil, nil, nil, nil)
}

func (p *Proxy) startStream(w http.ResponseWriter, c *caller) (*a2a.SSEWriter, bool) {
	sse, err := a2a.NewSSEWriter(w)
	if err != nil {
		writeHubError(w, &hubError{err: &a2a.Error{Code: a2a.CodeInternalError, Message: "Internal error"}, status: http.StatusInternalServerError})
		return nil, false
	}
	w.WriteHeader(http.StatusOK)
	hello := a2a.ControlHello{Epoch: p.epoch, Revision: uint64(c.st.revision), GroupGeneration: string(c.st.groupGeneration),
		Principals: c.st.principals()}
	return sse, writeEvent(sse, a2a.ControlEventHello, hello)
}

// keepalive writes comments until the request ends or is revoked; a2a
// clients count two missed keepalives as connection loss.
func (p *Proxy) keepalive(c *caller, sse *a2a.SSEWriter, changes <-chan struct{}, onChange func() bool,
	revisions <-chan struct{}, onRevision func() bool) {
	tick := time.NewTicker(p.cfg.KeepaliveInterval)
	defer tick.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-changes:
			if !onChange() {
				return
			}
		case <-revisions:
			if !onRevision() {
				return
			}
		case <-tick.C:
			if sse.WriteComment("keepalive") != nil {
				return
			}
		}
	}
}

func writeEvent(sse *a2a.SSEWriter, name string, v any) bool {
	data, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return sse.WriteEvent(a2a.Event{Event: name, Data: data}) == nil
}
