// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/peergroup"
)

const testGroup = "review"

var (
	testGroupGen = peergroup.Generation("3f9c0a1b2c3d4e5f")
	testHostGen  = peergroup.Generation("0011223344556677")
)

// fixture is a hub proxy over a temp state root with fake adapters.
type fixture struct {
	t         *testing.T
	stateRoot string
	pub       peergroup.SnapshotPublisher
	members   []peergroup.MembershipMember
	outbound  []peergroup.OutboundSecret
	tokens    map[string]string
	hostCreds []peergroup.CredentialHash
	cfg       Config
	proxy     *Proxy
	hub       *httptest.Server
	mu        sync.Mutex
	now       time.Time
}

func newFixture(t *testing.T, tweak func(*Config)) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		t:         t,
		stateRoot: root,
		pub:       peergroup.NewSnapshotPublisher(root, nil),
		tokens:    map[string]string{},
		now:       time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	}
	for _, dir := range []string{peergroup.SnapshotsDir(root, testGroup), peergroup.StatusDir(root, testGroup), peergroup.AuditDir(root, testGroup)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.cfg = Config{
		Group:             testGroup,
		GroupGeneration:   testGroupGen,
		SnapshotDir:       peergroup.SnapshotsDir(root, testGroup),
		StatusDir:         peergroup.StatusDir(root, testGroup),
		AuditDir:          peergroup.AuditDir(root, testGroup),
		KeepaliveInterval: time.Hour,
		Now:               f.clock,
	}
	if tweak != nil {
		tweak(&f.cfg)
	}
	p, err := New(f.cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.proxy = p
	f.hub = httptest.NewServer(p.Handler())
	t.Cleanup(func() {
		p.Close()
		f.hub.Close()
	})
	return f
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func newToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func newGen(t *testing.T) peergroup.Generation {
	t.Helper()
	g, err := peergroup.NewGeneration()
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func adapterAddr(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()
	host, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(port)
	return host, n
}

// addWorker adds a worker served by adapter and returns its outbound token.
func (f *fixture) addWorker(name string, adapter *httptest.Server) string {
	f.t.Helper()
	host, port := adapterAddr(f.t, adapter)
	inbound := newToken(f.t)
	outbound := newToken(f.t)
	gen := newGen(f.t)
	f.tokens[name] = inbound
	f.members = append(f.members, peergroup.MembershipMember{
		Name: name, Role: a2a.RoleWorker, Generation: gen, ProjectPath: "/src/" + name, MountMode: peergroup.MountReadWrite,
		HubAddress: "127.0.0.1", GatewayAddress: host, AdapterPort: port, InboundTokenHash: hashToken(inbound),
		Description: name + " reviews diffs", Tool: "claude",
		Skills: []a2a.AgentSkill{{ID: "review", Name: "Review", Description: "reviews diffs", Tags: []string{"review"}}},
	})
	f.outbound = append(f.outbound, peergroup.OutboundSecret{Member: name, Generation: gen, Token: outbound})
	return outbound
}

func (f *fixture) addOrchestrator(name string) {
	f.t.Helper()
	inbound := newToken(f.t)
	f.tokens[name] = inbound
	f.members = append(f.members, peergroup.MembershipMember{
		Name: name, Role: a2a.RoleOrchestrator, Generation: newGen(f.t), ProjectPath: "/src/" + name, MountMode: peergroup.MountReadWrite,
		HubAddress: "127.0.0.1", GatewayAddress: "127.0.0.1", InboundTokenHash: hashToken(inbound), Tool: "claude",
	})
}

// addHostCredential publishes a host invocation credential valid for lifetime.
func (f *fixture) addHostCredential(lifetime time.Duration) string {
	f.t.Helper()
	token := newToken(f.t)
	f.hostCreds = append(f.hostCreds, peergroup.CredentialHash{Hash: hashToken(token), ExpiresAt: peergroup.FormatTimestamp(f.clock().Add(lifetime))})
	return token
}

func (f *fixture) remove(name string) {
	for i, m := range f.members {
		if m.Name == name {
			f.members = append(f.members[:i], f.members[i+1:]...)
			break
		}
	}
	for i, o := range f.outbound {
		if o.Member == name {
			f.outbound = append(f.outbound[:i], f.outbound[i+1:]...)
			break
		}
	}
}

func (f *fixture) generation(name string) peergroup.Generation {
	for _, m := range f.members {
		if m.Name == name {
			return m.Generation
		}
	}
	f.t.Fatalf("no member %s", name)
	return ""
}

func (f *fixture) publish() peergroup.Revision {
	f.t.Helper()
	m := peergroup.Membership{
		Group: testGroup, GroupGeneration: testGroupGen, Members: append([]peergroup.MembershipMember(nil), f.members...),
		HostPrincipal: peergroup.HostPrincipalEntry{Name: peergroup.HostMemberName, Generation: testHostGen, Credentials: append([]peergroup.CredentialHash(nil), f.hostCreds...)},
	}
	s := peergroup.Secrets{Group: testGroup, GroupGeneration: testGroupGen, Outbound: append([]peergroup.OutboundSecret(nil), f.outbound...)}
	rev, err := f.pub.Publish(context.Background(), testGroup, m, s)
	if err != nil {
		f.t.Fatal(err)
	}
	return rev
}

func (f *fixture) publishAndReload() peergroup.Revision {
	f.t.Helper()
	rev := f.publish()
	if err := f.proxy.Reload(); err != nil {
		f.t.Fatalf("reload revision %d: %v", rev, err)
	}
	return rev
}

func (f *fixture) status() peergroup.StatusFile {
	f.t.Helper()
	_, proxy, err := peergroup.NewStatusWaiter(f.stateRoot).Read(testGroup)
	if err != nil {
		f.t.Fatal(err)
	}
	if proxy == nil {
		f.t.Fatal("no proxy status file")
	}
	return *proxy
}

func (f *fixture) auditEntries() []AuditEntry {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.cfg.AuditDir, peergroup.AuditLogFile))
	if err != nil {
		f.t.Fatal(err)
	}
	var out []AuditEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			f.t.Fatalf("audit line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

// request builds a hub request with the A2A headers set.
func (f *fixture) request(method, path, token, body string) *http.Request {
	f.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.hub.URL+path, rd)
	if err != nil {
		f.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set(a2a.HeaderAuthorization, a2a.BearerPrefix+token)
	}
	a2a.SetProtocolHeaders(req.Header)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func (f *fixture) do(req *http.Request) *http.Response {
	f.t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp
}

// rpc posts a JSON-RPC request and decodes a non-streaming response.
func (f *fixture) rpc(token, peer, body string) a2a.Response {
	f.t.Helper()
	resp := f.do(f.request(http.MethodPost, a2a.PeerPathPrefix+peer, token, body))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("status %d", resp.StatusCode)
	}
	var out a2a.Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func wantCode(t *testing.T, resp a2a.Response, code int, reason string) {
	t.Helper()
	if resp.Error == nil {
		t.Fatalf("want error %d, got result %s", code, resp.Result)
	}
	if resp.Error.Code != code {
		t.Fatalf("want code %d, got %d (%s)", code, resp.Error.Code, resp.Error.Message)
	}
	if reason != "" {
		info, ok := resp.Error.Info()
		if !ok || info.Reason != reason {
			t.Fatalf("want reason %s, got %+v", reason, info)
		}
	}
}

// recorder is a fake adapter that records requests and answers with handle.
type recorder struct {
	mu       sync.Mutex
	requests []recorded
	handle   func(w http.ResponseWriter, r *http.Request, body []byte)
}

type recorded struct {
	path   string
	header http.Header
	body   []byte
}

func newAdapter(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body []byte)) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{handle: handle}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.requests = append(rec.requests, recorded{path: r.URL.Path, header: r.Header.Clone(), body: body})
		rec.mu.Unlock()
		rec.handle(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func (r *recorder) last() recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[len(r.requests)-1]
}

// taskResult answers every request with a task result for the given state.
func taskResult(id string, state a2a.TaskState) func(w http.ResponseWriter, r *http.Request, body []byte) {
	return func(w http.ResponseWriter, r *http.Request, body []byte) {
		req, _ := a2a.DecodeRequest(body)
		task := a2a.Task{ID: id, ContextID: "ctx-1", Status: a2a.TaskStatus{State: state}}
		var result json.RawMessage
		if req.Method == a2a.MethodGetTask || req.Method == a2a.MethodCancelTask {
			result, _ = json.Marshal(task)
		} else {
			result, _ = a2a.EncodeResult(a2a.Result{Task: &task})
		}
		writeJSON(w, http.StatusOK, a2a.Response{JSONRPC: "2.0", ID: req.ID, Result: result})
	}
}

func sendBody(id int, text string) string {
	return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"SendMessage","params":{"message":{"messageId":"m-` +
		strconv.Itoa(id) + `","role":"ROLE_USER","parts":[{"text":` + strconv.Quote(text) + `}]}}}`
}

func getTaskBody(id int, task string) string {
	return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"GetTask","params":{"id":"` + task + `"}}`
}

// sseEvents reads SSE events (data joined) from r until EOF.
type sseClient struct {
	resp *http.Response
	r    *a2a.SSEReader
}

func openStream(t *testing.T, f *fixture, req *http.Request) *sseClient {
	t.Helper()
	resp := f.do(req)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream status %d: %s", resp.StatusCode, body)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return &sseClient{resp: resp, r: a2a.NewSSEReader(bufio.NewReader(resp.Body), 0)}
}

func (s *sseClient) next(t *testing.T) a2a.Event {
	t.Helper()
	type res struct {
		ev  a2a.Event
		err error
	}
	ch := make(chan res, 1)
	go func() {
		ev, err := s.r.Next()
		ch <- res{ev, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("stream: %v", r.err)
		}
		return r.ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an event")
	}
	return a2a.Event{}
}

func (s *sseClient) expectEOF(t *testing.T) {
	t.Helper()
	ch := make(chan error, 1)
	go func() {
		_, err := s.r.Next()
		ch <- err
	}()
	select {
	case err := <-ch:
		if err != io.EOF {
			t.Fatalf("want EOF, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream not closed")
	}
}
