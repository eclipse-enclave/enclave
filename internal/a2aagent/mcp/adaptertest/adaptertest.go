// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package adaptertest runs the real worker adapter with the fake harness so
// client tests decode the adapter's actual responses. Test binaries using it
// must call fakedriver.Main first in TestMain; the executor needs Linux.
package adaptertest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/a2aagent/contexttable"
	"enclave/internal/a2aagent/executor"
	"enclave/internal/a2aagent/executor/fakedriver"
	"enclave/internal/a2aagent/server"
)

const (
	epoch         = "1111111111111111"
	outboundToken = "outbound-token"
)

// Options names the group and the members the adapter accepts as callers.
type Options struct {
	Group, GroupGeneration   string
	Worker, WorkerGeneration string
	// Mode is the fakedriver harness mode.
	Mode string
	// Callers are the non-worker members announced in the control hello.
	Callers []a2a.Principal
}

// Adapter is a running adapter reachable as the hub would forward to it.
type Adapter struct {
	url string
	opt Options
	srv *server.Server
}

// Start runs the adapter until the test ends and waits for its control connection.
func Start(t testing.TB, opt Options) *Adapter {
	t.Helper()
	var principals []a2a.MemberPrincipal
	for _, p := range opt.Callers {
		principals = append(principals, a2a.MemberPrincipal{Member: p.Member, Role: p.Role, Generation: p.Generation})
	}
	hub := httptest.NewServer(controlHub(opt.GroupGeneration, principals))
	hubHost, hubPortText, _ := net.SplitHostPort(hub.Listener.Addr().String())
	hubPort, _ := strconv.Atoi(hubPortText)

	tableDir := filepath.Join(t.TempDir(), "a2a", opt.WorkerGeneration)
	cfg := a2aagent.Config{
		Role: a2a.RoleWorker, Group: opt.Group, GroupGeneration: opt.GroupGeneration,
		Member: opt.Worker, MemberGeneration: opt.WorkerGeneration,
		HubHost: hubHost, HubPort: hubPort, InboundToken: "inbound-token", OutboundToken: outboundToken,
		TableDir: tableDir, QueueDepth: 4, ReconnectWindow: 10 * time.Second,
	}
	tbl, err := contexttable.Open(contexttable.Options{
		Dir:        tableDir,
		Header:     a2aagent.TableHeader{Group: cfg.Group, GroupGeneration: cfg.GroupGeneration, Worker: cfg.Member, WorkerGeneration: cfg.MemberGeneration},
		QueueDepth: cfg.QueueDepth,
	})
	if err != nil {
		t.Fatalf("open table: %v", err)
	}
	ex, err := executor.New(executor.Options{
		Table: tbl, Driver: &fakedriver.Driver{Exe: os.Args[0], Mode: opt.Mode},
		Group: cfg.Group, GroupGeneration: cfg.GroupGeneration, Worker: cfg.Member, WorkerGeneration: cfg.MemberGeneration,
		WorkDir: t.TempDir(), TaskDeadline: 20 * time.Second, TerminationTimeout: time.Second,
		PollInterval: 20 * time.Millisecond, Log: io.Discard,
	})
	if err != nil {
		t.Fatalf("executor: %v", err)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// Unix socket paths are short; t.TempDir can exceed the limit.
	sockDir, err := os.MkdirTemp("", "a2a-ctl")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(server.Options{
		Config: cfg, Table: tbl, Executor: ex, Version: "test", Listener: ln,
		CtlSocket: filepath.Join(sockDir, "ctl.sock"), KeepaliveInterval: 50 * time.Millisecond,
		EpochWait: 2 * time.Second, Log: io.Discard,
	})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("adapter Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("adapter Serve did not return")
		}
		hub.CloseClientConnections()
		hub.Close()
		_ = os.RemoveAll(sockDir)
	})
	deadline := time.Now().Add(10 * time.Second)
	for srv.ControlStatus() != server.ControlConnected {
		if time.Now().After(deadline) {
			t.Fatal("adapter control connection did not come up")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return &Adapter{url: "http://" + ln.Addr().String(), opt: opt, srv: srv}
}

// Post forwards a JSON-RPC body from caller with the headers the hub proxy sets.
func (a *Adapter) Post(ctx context.Context, caller a2a.Principal, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url+a2a.AdapterRPCPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	h := req.Header
	h.Set(a2a.HeaderAuthorization, a2a.BearerPrefix+outboundToken)
	a2a.SetProtocolHeaders(h)
	h.Set("Content-Type", "application/json")
	h.Set(a2a.HeaderGroup, a.opt.Group)
	h.Set(a2a.HeaderGroupGeneration, a.opt.GroupGeneration)
	h.Set(a2a.HeaderMember, caller.Member)
	h.Set(a2a.HeaderRole, string(caller.Role))
	h.Set(a2a.HeaderGeneration, caller.Generation)
	h.Set(a2a.HeaderEpoch, epoch)
	h.Set(a2a.HeaderRevision, "1")
	return http.DefaultClient.Do(req) // #nosec G704 -- loopback test adapter.
}

// Handler serves POSTs as the hub's peer route would, as caller.
func (a *Adapter) Handler(caller a2a.Principal) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp, err := a.Post(r.Context(), caller, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				return
			}
		}
	})
}

// controlHub answers the adapter's control stream with a hello and keepalives.
func controlHub(groupGeneration string, principals []a2a.MemberPrincipal) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != a2a.HubControlPath {
			http.NotFound(w, r)
			return
		}
		sw, err := a2a.NewSSEWriter(w)
		if err != nil {
			return
		}
		hello, _ := json.Marshal(a2a.ControlHello{Epoch: epoch, Revision: 1, GroupGeneration: groupGeneration, Principals: principals})
		if sw.WriteEvent(a2a.Event{Event: a2a.ControlEventHello, Data: hello}) != nil {
			return
		}
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				if sw.WriteComment("keepalive") != nil {
					return
				}
			}
		}
	})
}
