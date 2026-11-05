// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/a2aagent/contexttable"
	"enclave/internal/a2aagent/executor"
	"enclave/internal/a2aagent/executor/fakedriver"
)

func TestMain(m *testing.M) {
	fakedriver.Main()
	os.Exit(m.Run())
}

const (
	testGroupGeneration  = "00000000000000ff"
	testMemberGeneration = "0000000000000abc"
	testEpoch            = "1111111111111111"
	testKeepalive        = 50 * time.Millisecond
	terminationTimeout   = 300 * time.Millisecond
)

var (
	orch = a2a.Principal{Group: "review", GroupGeneration: testGroupGeneration, Member: "orch",
		Role: a2a.RoleOrchestrator, Generation: "0000000000000001"}
	host = a2a.Principal{Group: "review", GroupGeneration: testGroupGeneration, Member: HostMemberName,
		Role: a2a.RoleHost, Generation: "00000000000000aa"}
)

// defaultPrincipals is the membership the fake hub reports: orch, orch-2 (same generation) and the host.
func defaultPrincipals() []a2a.MemberPrincipal {
	return []a2a.MemberPrincipal{
		{Member: orch.Member, Role: a2a.RoleOrchestrator, Generation: orch.Generation},
		{Member: "orch-2", Role: a2a.RoleOrchestrator, Generation: orch.Generation},
		{Member: host.Member, Role: a2a.RoleHost, Generation: host.Generation},
	}
}

func refOf(p a2a.Principal) a2aagent.PrincipalRef {
	return a2aagent.PrincipalRef{Member: p.Member, Role: p.Role, Generation: p.Generation}
}

// fakeHub serves the control stream: hello, pushed events and keepalive comments.
type fakeHub struct {
	srv *httptest.Server

	mu       sync.Mutex
	epoch    string
	groupGen string
	// principals is sent in hello.
	principals []a2a.MemberPrincipal
	refuse     bool
	silent     bool
	conns      int
	kick       chan struct{}
	events     chan a2a.Event
	authSeen   []string
}

func newFakeHub(t *testing.T) *fakeHub {
	h := &fakeHub{epoch: testEpoch, groupGen: testGroupGeneration, principals: defaultPrincipals(),
		kick: make(chan struct{}), events: make(chan a2a.Event, 16)}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(func() {
		h.kickAll()
		h.srv.CloseClientConnections()
		h.srv.Close()
	})
	return h
}

func (h *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != a2a.HubControlPath {
		http.NotFound(w, r)
		return
	}
	h.mu.Lock()
	h.authSeen = append(h.authSeen, r.Header.Get(a2a.HeaderAuthorization))
	refuse, epoch, groupGen, principals, kick := h.refuse, h.epoch, h.groupGen, h.principals, h.kick
	if !refuse {
		h.conns++
	}
	h.mu.Unlock()
	if refuse {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	sw, err := a2a.NewSSEWriter(w)
	if err != nil {
		return
	}
	hello, _ := json.Marshal(a2a.ControlHello{Epoch: epoch, Revision: 1, GroupGeneration: groupGen, Principals: principals})
	if sw.WriteEvent(a2a.Event{Event: a2a.ControlEventHello, Data: hello}) != nil {
		return
	}
	tick := time.NewTicker(testKeepalive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-kick:
			return
		case ev := <-h.events:
			if sw.WriteEvent(ev) != nil {
				return
			}
		case <-tick.C:
			h.mu.Lock()
			silent := h.silent
			h.mu.Unlock()
			if !silent && sw.WriteComment("keepalive") != nil {
				return
			}
		}
	}
}

func (h *fakeHub) kickAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	close(h.kick)
	h.kick = make(chan struct{})
}

func (h *fakeHub) set(f func(h *fakeHub)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f(h)
}

func (h *fakeHub) connections() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns
}

func (h *fakeHub) pushOrchestrator(st a2a.OrchestratorState) {
	data, _ := json.Marshal(st)
	h.events <- a2a.Event{Event: a2a.ControlEventOrchestrator, Data: data}
}

func (h *fakeHub) pushMembership(m a2a.ControlMembership) {
	data, _ := json.Marshal(m)
	h.events <- a2a.Event{Event: a2a.ControlEventMembership, Data: data}
}

func (h *fakeHub) hostPort() (string, int) {
	host, port, _ := net.SplitHostPort(h.srv.Listener.Addr().String())
	n, _ := strconv.Atoi(port)
	return host, n
}

// fakeExec records control-plane calls; it runs nothing.
type fakeExec struct {
	mu      sync.Mutex
	epochs  []string
	hubLost int
	departs []departCall
	alive   bool
}

type departCall struct {
	ref           a2aagent.PrincipalRef
	authoritative bool
}

func (f *fakeExec) Run(ctx context.Context) error { <-ctx.Done(); return nil }
func (f *fakeExec) Submit(string, string)         {}
func (f *fakeExec) HubLost(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hubLost++
	return nil
}
func (f *fakeExec) Subscribe(string) (<-chan a2aagent.TaskEvent, func()) {
	ch := make(chan a2aagent.TaskEvent)
	close(ch)
	return ch, func() {}
}
func (f *fakeExec) Cancel(context.Context, string) (*a2aagent.TaskRecord, error) {
	return nil, errors.New("not implemented by the fake")
}
func (f *fakeExec) EpochChanged(_ context.Context, epoch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.epochs = append(f.epochs, epoch)
	return nil
}
func (f *fakeExec) Depart(_ context.Context, ref a2aagent.PrincipalRef, authoritative bool) (a2a.DepartureAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.departs = append(f.departs, departCall{ref, authoritative})
	return a2a.DepartureAck{AdmissionStopPersisted: true, QueuedDiscarded: []string{}, RunningTerminated: []string{},
		Termination: a2a.TerminationNotApplicable}, nil
}
func (f *fakeExec) DescendantsAlive() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive, nil
}

type fakeCalls struct {
	epochs  []string
	hubLost int
	departs []departCall
}

func (f *fakeExec) snapshot() fakeCalls {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeCalls{epochs: append([]string{}, f.epochs...), hubLost: f.hubLost, departs: append([]departCall{}, f.departs...)}
}

type env struct {
	t      *testing.T
	hub    *fakeHub
	tbl    *contexttable.Table
	ex     a2aagent.Executor
	srv    *Server
	url    string
	socket string
	log    *syncBuffer
	cancel context.CancelFunc
	done   chan error
}

type envSetup struct {
	mode     string    // fake harness mode; empty uses fakeExec
	fake     *fakeExec // used when mode is empty
	tweakCfg func(*a2aagent.Config)
	tweak    func(*Options)
	hub      func(*fakeHub) // runs before Serve
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newEnv(t *testing.T, s envSetup) *env {
	t.Helper()
	e := &env{t: t, hub: newFakeHub(t), log: &syncBuffer{}}
	if s.hub != nil {
		e.hub.set(s.hub)
	}
	hubHost, hubPort := e.hub.hostPort()
	tableDir := filepath.Join(t.TempDir(), "a2a", testMemberGeneration)
	cfg := a2aagent.Config{
		Role: a2a.RoleWorker, Group: "review", GroupGeneration: testGroupGeneration,
		Member: "worker-1", MemberGeneration: testMemberGeneration,
		HubHost: hubHost, HubPort: hubPort, InboundToken: "inbound-token", OutboundToken: "outbound-token",
		TableDir: tableDir, Description: "reviews diffs", QueueDepth: 4,
		ReconnectWindow: 10 * time.Second,
	}
	if s.tweakCfg != nil {
		s.tweakCfg(&cfg)
	}
	tbl, err := contexttable.Open(contexttable.Options{
		Dir:        tableDir,
		Header:     a2aagent.TableHeader{Group: cfg.Group, GroupGeneration: cfg.GroupGeneration, Worker: cfg.Member, WorkerGeneration: cfg.MemberGeneration},
		QueueDepth: cfg.QueueDepth,
	})
	if err != nil {
		t.Fatalf("open table: %v", err)
	}
	e.tbl = tbl
	if s.mode != "" {
		ex, err := executor.New(executor.Options{
			Table: tbl, Driver: &fakedriver.Driver{Exe: os.Args[0], Mode: s.mode},
			Group: cfg.Group, GroupGeneration: cfg.GroupGeneration, Worker: cfg.Member, WorkerGeneration: cfg.MemberGeneration,
			WorkDir: t.TempDir(), TaskDeadline: 20 * time.Second, TerminationTimeout: terminationTimeout,
			PollInterval: 20 * time.Millisecond, Log: e.log,
		})
		if err != nil {
			t.Fatalf("executor: %v", err)
		}
		e.ex = ex
	} else {
		if s.fake == nil {
			s.fake = &fakeExec{}
		}
		e.ex = s.fake
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	sockDir, err := os.MkdirTemp("", "a2a-ctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	e.socket = filepath.Join(sockDir, "ctl.sock")
	opts := Options{
		Config: cfg, Table: tbl, Executor: e.ex, Version: "test",
		Listener: ln, CtlSocket: e.socket, KeepaliveInterval: testKeepalive, EpochWait: 2 * time.Second, Log: e.log,
	}
	if s.tweak != nil {
		s.tweak(&opts)
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.srv = srv
	e.url = "http://" + ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan error, 1)
	go func() { e.done <- srv.Serve(ctx) }()
	t.Cleanup(e.stop)
	waitFor(t, "ctl socket", func() bool { _, err := os.Stat(e.socket); return err == nil })
	return e
}

func (e *env) stop() {
	if e.cancel == nil {
		return
	}
	e.cancel()
	e.cancel = nil
	select {
	case err := <-e.done:
		if err != nil {
			e.t.Errorf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		e.t.Errorf("Serve did not return")
	}
	if e.t.Failed() {
		e.t.Logf("adapter log:\n%s", e.log.String())
	}
}

func (e *env) waitConnected() {
	e.t.Helper()
	waitFor(e.t, "control connection", func() bool { return e.srv.ControlStatus() == ControlConnected })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type request struct {
	principal *a2a.Principal
	epoch     string
	headers   map[string]string
	drop      []string
	body      []byte
}

// post sends a JSON-RPC request as the hub would forward it.
func (e *env) post(method string, params any, r request) *http.Response {
	e.t.Helper()
	body := r.body
	if body == nil {
		raw, err := json.Marshal(params)
		if err != nil {
			e.t.Fatal(err)
		}
		body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": json.RawMessage(raw)})
	}
	req, err := http.NewRequest(http.MethodPost, e.url+a2a.AdapterRPCPath, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	p := orch
	if r.principal != nil {
		p = *r.principal
	}
	epoch := r.epoch
	if epoch == "" {
		epoch = testEpoch
	}
	h := req.Header
	h.Set(a2a.HeaderAuthorization, a2a.BearerPrefix+"outbound-token")
	a2a.SetProtocolHeaders(h)
	h.Set(a2a.HeaderGroup, p.Group)
	h.Set(a2a.HeaderGroupGeneration, p.GroupGeneration)
	h.Set(a2a.HeaderMember, p.Member)
	h.Set(a2a.HeaderRole, string(p.Role))
	h.Set(a2a.HeaderGeneration, p.Generation)
	h.Set(a2a.HeaderEpoch, epoch)
	h.Set(a2a.HeaderRevision, "1")
	for k, v := range r.headers {
		h.Set(k, v)
	}
	for _, k := range r.drop {
		h.Del(k)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("post %s: %v", method, err)
	}
	return resp
}

// call posts and decodes a single JSON-RPC response.
func (e *env) call(method string, params any, r request) a2a.Response {
	e.t.Helper()
	resp := e.post(method, params, r)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("%s: HTTP %d", method, resp.StatusCode)
	}
	var out a2a.Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		e.t.Fatalf("%s: decode: %v", method, err)
	}
	return out
}

// stream posts and returns every SSE response until the server closes the stream.
func (e *env) stream(method string, params any, r request) []a2a.Response {
	e.t.Helper()
	var out []a2a.Response
	for resp := range e.streamChan(method, params, r) {
		out = append(out, resp)
	}
	return out
}

func (e *env) streamChan(method string, params any, r request) <-chan a2a.Response {
	e.t.Helper()
	resp := e.post(method, params, r)
	out := make(chan a2a.Response, 64)
	if ct := resp.Header.Get("Content-Type"); ct != a2a.SSEContentType {
		defer func() { _ = resp.Body.Close() }()
		var single a2a.Response
		if err := json.NewDecoder(resp.Body).Decode(&single); err != nil {
			e.t.Fatalf("%s: not a stream (%s) and not JSON: %v", method, ct, err)
		}
		out <- single
		close(out)
		return out
	}
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		rd := a2a.NewSSEReader(resp.Body, 0)
		for {
			ev, err := rd.Next()
			if err != nil {
				return
			}
			var r a2a.Response
			if json.Unmarshal(ev.Data, &r) == nil {
				out <- r
			}
		}
	}()
	return out
}

func result(t *testing.T, resp a2a.Response) a2a.Result {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	r, err := a2a.DecodeResult(resp.Result)
	if err != nil {
		t.Fatalf("decode result %s: %v", resp.Result, err)
	}
	return r
}

func task(t *testing.T, resp a2a.Response) a2a.Task {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	var tk a2a.Task
	if err := json.Unmarshal(resp.Result, &tk); err != nil {
		t.Fatalf("decode task %s: %v", resp.Result, err)
	}
	return tk
}

func meta(t *testing.T, metadata map[string]any) a2a.EnclaveMeta {
	t.Helper()
	m, _, err := a2a.EnclaveMetaFrom(metadata)
	if err != nil {
		t.Fatalf("enclave metadata: %v", err)
	}
	return m
}

func wantError(t *testing.T, resp a2a.Response, code int, reason string) {
	t.Helper()
	if resp.Error == nil {
		t.Fatalf("want error %d, got result %s", code, resp.Result)
	}
	if resp.Error.Code != code {
		t.Fatalf("error code = %d (%s), want %d", resp.Error.Code, resp.Error.Message, code)
	}
	info, ok := resp.Error.Info()
	enclaveRange := code >= -31099 && code <= -31000
	switch {
	case enclaveRange && (!ok || info.Reason != reason):
		t.Fatalf("error %d: ErrorInfo = %+v (%v), want reason %q", code, info, ok, reason)
	case !enclaveRange && len(resp.Error.Data) != 0:
		t.Fatalf("error %d must carry no data, got %s", code, resp.Error.Data)
	}
}

func message(id, text string) a2a.Message {
	return a2a.Message{MessageID: id, Role: a2a.RoleUser, Parts: []a2a.Part{a2a.TextPart(text)}}
}

func sendParams(m a2a.Message) a2a.SendMessageRequest { return a2a.SendMessageRequest{Message: m} }
