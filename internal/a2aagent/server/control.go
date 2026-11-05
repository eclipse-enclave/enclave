// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
)

const (
	controlMaxEventBytes = 64 << 10
	controlBackoffMin    = 100 * time.Millisecond
	controlBackoffMax    = 5 * time.Second
)

// ControlStatus is the control connection state (see the A10 report).
type ControlStatus string

const (
	// ControlConnecting: no hello seen since the adapter started or since the hub was declared lost.
	ControlConnecting ControlStatus = "connecting"
	// ControlConnected: a hello was accepted and keepalives arrive.
	ControlConnected ControlStatus = "connected"
	// ControlReconnecting: the connection dropped; the reconnect window runs.
	ControlReconnecting ControlStatus = "reconnecting"
	// ControlHubLost: the window elapsed and HubLost ended running and queued work.
	ControlHubLost ControlStatus = "hub-lost"
)

type controlState struct {
	status    ControlStatus
	lastEpoch string
	// seq invalidates reconnect-window timers armed before a later hello.
	seq           uint64
	windowTimer   *time.Timer
	orchestrators map[string]*orchestratorState
	// members holds the principals of the newest membership the hub sent (CR-A10-4); nil until the first hello.
	members          map[string]a2a.MemberPrincipal
	membersRevision  uint64
	membershipSignal chan struct{}
}

// orchestratorState tracks one orchestrator's liveness as the hub reports it (P13).
type orchestratorState struct {
	generation string
	connected  bool
	suspected  bool
	timer      *time.Timer
}

// controlEvent is one state transition, applied in order by the dispatcher.
type controlEvent struct {
	hello        *a2a.ControlHello
	orchestrator *a2a.OrchestratorState
	membership   *a2a.ControlMembership
	lost         bool
}

func newHubClient(keepalive time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Client{Transport: &http.Transport{
		// The hub is IPv4 only (P8).
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", addr)
		},
		ResponseHeaderTimeout: 2 * keepalive,
	}}
}

// ControlStatus reports the control connection state.
func (s *Server) ControlStatus() ControlStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctl.status
}

// runControl keeps the control connection up; one dispatcher keeps events in order while EpochChanged blocks.
func (s *Server) runControl(ctx context.Context) {
	events := make(chan controlEvent, 64)
	dispatched := make(chan struct{})
	go func() {
		defer close(dispatched)
		for {
			select {
			case ev := <-events:
				s.apply(ctx, ev)
			case <-ctx.Done():
				return
			}
		}
	}()
	s.mu.Lock()
	s.ctl.status = ControlConnecting
	s.armWindowLocked(ctx)
	s.mu.Unlock()

	backoff := controlBackoffMin
	for ctx.Err() == nil {
		helloSeen, err := s.controlOnce(ctx, events)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			s.logf("control connection: %v", err)
		}
		if helloSeen {
			backoff = controlBackoffMin
			select {
			case events <- controlEvent{lost: true}:
			case <-ctx.Done():
			}
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
		}
		backoff = min(2*backoff, controlBackoffMax)
	}
	<-dispatched
}

// controlOnce runs one connection; helloSeen reports whether a hello was accepted.
func (s *Server) controlOnce(ctx context.Context, events chan<- controlEvent) (helloSeen bool, err error) {
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	url := "http://" + net.JoinHostPort(s.cfg.HubHost, strconv.Itoa(s.cfg.HubPort)) + a2a.HubControlPath
	req, err := http.NewRequestWithContext(connCtx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set(a2a.HeaderAuthorization, a2a.BearerPrefix+s.cfg.InboundToken)
	req.Header.Set("Accept", a2a.SSEContentType)
	a2a.SetProtocolHeaders(req.Header)
	resp, err := s.hubClient.Do(req) // #nosec G704 -- the hub address comes from the host-written session env.
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("hub answered %s", resp.Status)
	}

	// Any byte, keepalive comments included, proves the connection; 2 silent intervals are a loss.
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var silent atomic.Bool
	go func() {
		tick := time.NewTicker(max(s.keepalive/4, time.Millisecond))
		defer tick.Stop()
		for {
			select {
			case <-connCtx.Done():
				return
			case <-tick.C:
				if time.Since(time.Unix(0, last.Load())) > 2*s.keepalive {
					silent.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	body := &activityReader{r: resp.Body, touch: func() { last.Store(time.Now().UnixNano()) }}
	reader := a2a.NewSSEReader(body, controlMaxEventBytes)
	defer func() {
		if silent.Load() {
			err = errors.New("keepalive missing for 2 intervals")
		}
	}()

	ev, err := reader.Next()
	if err != nil {
		return false, err
	}
	if ev.Event != a2a.ControlEventHello {
		return false, fmt.Errorf("first control event is %q, not hello", ev.Event)
	}
	var hello a2a.ControlHello
	if err := json.Unmarshal(ev.Data, &hello); err != nil {
		return false, fmt.Errorf("hello: %w", err)
	}
	if hello.GroupGeneration != s.cfg.GroupGeneration {
		return false, fmt.Errorf("hello names group generation %q, want %q", hello.GroupGeneration, s.cfg.GroupGeneration)
	}
	if !generationPattern.MatchString(hello.Epoch) {
		return false, fmt.Errorf("hello carries an invalid epoch %q", hello.Epoch)
	}
	select {
	case events <- controlEvent{hello: &hello}:
	case <-ctx.Done():
		return true, nil
	}
	for {
		ev, err := reader.Next()
		if err != nil {
			return true, err
		}
		switch ev.Event {
		case a2a.ControlEventOrchestrator:
			var st a2a.OrchestratorState
			if err := json.Unmarshal(ev.Data, &st); err != nil || st.Member == "" || !generationPattern.MatchString(st.Generation) {
				s.logf("control connection: ignoring malformed orchestrator event")
				continue
			}
			select {
			case events <- controlEvent{orchestrator: &st}:
			case <-ctx.Done():
				return true, nil
			}
		case a2a.ControlEventMembership:
			var m a2a.ControlMembership
			if err := json.Unmarshal(ev.Data, &m); err != nil {
				s.logf("control connection: ignoring malformed membership event")
				continue
			}
			select {
			case events <- controlEvent{membership: &m}:
			case <-ctx.Done():
				return true, nil
			}
		default:
			s.logf("control connection: ignoring event %q", ev.Event)
		}
	}
}

type activityReader struct {
	r     io.Reader
	touch func()
}

func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.touch()
	}
	return n, err
}

func (s *Server) apply(ctx context.Context, ev controlEvent) {
	switch {
	case ev.hello != nil:
		s.onHello(ctx, *ev.hello)
	case ev.orchestrator != nil:
		s.onOrchestrator(ctx, *ev.orchestrator)
	case ev.membership != nil:
		s.setMembership(ev.membership.Revision, ev.membership.Principals, false)
	case ev.lost:
		s.mu.Lock()
		if s.ctl.status == ControlConnected {
			s.ctl.status = ControlReconnecting
			s.armWindowLocked(ctx)
		}
		s.mu.Unlock()
		s.logf("control connection lost; reconnect window %s", s.reconnectWindow)
	}
}

// onHello is the epoch handshake: old-epoch work ends before new-epoch sends are admitted (P14).
func (s *Server) onHello(ctx context.Context, hello a2a.ControlHello) {
	// A new hub process may restart revisions, so hello always replaces the membership.
	s.setMembership(hello.Revision, hello.Principals, true)
	s.mu.Lock()
	s.ctl.seq++
	if s.ctl.windowTimer != nil {
		s.ctl.windowTimer.Stop()
		s.ctl.windowTimer = nil
	}
	changed := hello.Epoch != s.ctl.lastEpoch
	s.mu.Unlock()
	if changed {
		s.setEpoch("")
		s.logf("hub epoch %s (revision %d)", hello.Epoch, hello.Revision)
		if err := s.exec.EpochChanged(ctx, hello.Epoch); err != nil {
			s.logf("epoch change: %v", err)
		}
	}
	s.mu.Lock()
	s.ctl.lastEpoch = hello.Epoch
	s.ctl.status = ControlConnected
	s.mu.Unlock()
	s.setEpoch(hello.Epoch)
}

// armWindowLocked starts the reconnect window; past it the adapter ends its work with hub-lost (P12, P14).
func (s *Server) armWindowLocked(ctx context.Context) {
	if s.ctl.windowTimer != nil {
		s.ctl.windowTimer.Stop()
	}
	seq := s.ctl.seq
	s.ctl.windowTimer = time.AfterFunc(s.reconnectWindow, func() {
		s.mu.Lock()
		if s.ctl.seq != seq || ctx.Err() != nil {
			s.mu.Unlock()
			return
		}
		s.ctl.windowTimer = nil
		s.ctl.status = ControlHubLost
		s.mu.Unlock()
		s.setEpoch("")
		s.logf("hub lost: no control connection for %s", s.reconnectWindow)
		if err := s.exec.HubLost(ctx); err != nil {
			s.logf("hub lost: %v", err)
		}
	})
}

// onOrchestrator treats a liveness drop that outlasts the reconnect window as a suspected departure (P13).
func (s *Server) onOrchestrator(ctx context.Context, st a2a.OrchestratorState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.ctl.orchestrators[st.Member]
	if o == nil || o.generation != st.Generation {
		if o != nil && o.timer != nil {
			o.timer.Stop()
		}
		o = &orchestratorState{generation: st.Generation}
		s.ctl.orchestrators[st.Member] = o
	}
	o.connected = st.Connected
	if st.Connected {
		if o.timer != nil {
			o.timer.Stop()
			o.timer = nil
		}
		return
	}
	if o.timer != nil || o.suspected {
		return
	}
	member := st.Member
	o.timer = time.AfterFunc(s.reconnectWindow, func() { s.suspect(ctx, member, o) })
}

func (s *Server) suspect(ctx context.Context, member string, o *orchestratorState) {
	s.mu.Lock()
	if s.ctl.orchestrators[member] != o || o.connected || o.suspected || ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	o.suspected = true
	o.timer = nil
	s.mu.Unlock()
	ref := a2aagent.PrincipalRef{Member: member, Role: a2a.RoleOrchestrator, Generation: o.generation}
	ack, err := s.exec.Depart(ctx, ref, false)
	if err != nil {
		s.logf("suspected departure of %s: %v", member, err)
		return
	}
	s.logf("suspected departure of %s (generation %s): admission stopped, %d queued task(s) discarded",
		member, o.generation, len(ack.QueuedDiscarded))
}

// setMembership replaces the known principals unless a newer revision is already known.
func (s *Server) setMembership(revision uint64, principals []a2a.MemberPrincipal, fromHello bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !fromHello && s.ctl.members != nil && revision < s.ctl.membersRevision {
		return
	}
	members := make(map[string]a2a.MemberPrincipal, len(principals))
	for _, p := range principals {
		members[p.Member] = p
	}
	s.ctl.members = members
	s.ctl.membersRevision = revision
	close(s.ctl.membershipSignal)
	s.ctl.membershipSignal = make(chan struct{})
}

// principalCurrent waits up to the epoch wait for a membership at least as new
// as revision, so a principal the hub just admitted is not refused; known is
// false when no membership arrived at all.
func (s *Server) principalCurrent(ctx context.Context, p a2a.Principal, revision uint64) (current, known bool) {
	bound := time.NewTimer(s.epochWait)
	defer bound.Stop()
	for {
		s.mu.Lock()
		members, rev, signal := s.ctl.members, s.ctl.membersRevision, s.ctl.membershipSignal
		s.mu.Unlock()
		waiting := members == nil || rev < revision
		if waiting {
			select {
			case <-signal:
				continue
			case <-bound.C:
			case <-ctx.Done():
			}
			s.mu.Lock()
			members = s.ctl.members
			s.mu.Unlock()
		}
		if members == nil {
			return false, false
		}
		m, ok := members[p.Member]
		return ok && m.Role == p.Role && m.Generation == p.Generation, true
	}
}

// orchestratorGenerationKnown refuses a generation the hub reported as superseded.
func (s *Server) orchestratorGenerationKnown(member, generation string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.ctl.orchestrators[member]
	return o == nil || o.generation == generation
}

// markDeparted stops suspicion for a principal the host already departed.
func (s *Server) markDeparted(ref a2aagent.PrincipalRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.ctl.orchestrators[ref.Member]; o != nil && o.generation == ref.Generation {
		if o.timer != nil {
			o.timer.Stop()
			o.timer = nil
		}
		o.suspected = true
	}
}

func (s *Server) stopTimers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctl.windowTimer != nil {
		s.ctl.windowTimer.Stop()
		s.ctl.windowTimer = nil
	}
	for _, o := range s.ctl.orchestrators {
		if o.timer != nil {
			o.timer.Stop()
			o.timer = nil
		}
	}
}
