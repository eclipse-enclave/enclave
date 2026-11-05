// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux

package mcp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/a2aagent/executor/fakedriver"
	"enclave/internal/a2aagent/mcp/adaptertest"
)

func TestMain(m *testing.M) {
	fakedriver.Main()
	os.Exit(m.Run())
}

// TestGetAndCancelAgainstRealAdapter decodes the adapter's own GetTask and CancelTask responses.
func TestGetAndCancelAgainstRealAdapter(t *testing.T) {
	lead := a2a.Principal{Group: "review", GroupGeneration: "aaaaaaaaaaaaaaaa", Member: "lead",
		Role: a2a.RoleOrchestrator, Generation: "bbbbbbbbbbbbbbbb"}
	adapter := adaptertest.Start(t, adaptertest.Options{
		Group: lead.Group, GroupGeneration: lead.GroupGeneration,
		Worker: testWorker, WorkerGeneration: testWorkerG,
		Mode: fakedriver.ModeHang, Callers: []a2a.Principal{lead},
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+a2a.HubPeersPath, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(a2a.PeerListing{Group: lead.Group, Peers: []a2a.PeerEntry{{Name: testWorker, Generation: testWorkerG}}})
	})
	mux.Handle("POST "+a2a.PeerPathPrefix+testWorker, adapter.Handler(lead))
	hub := httptest.NewServer(mux)
	t.Cleanup(hub.Close)
	host, port, _ := net.SplitHostPort(hub.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	c, err := New(Options{JournalDir: t.TempDir(), Config: a2aagent.Config{
		Role: a2a.RoleOrchestrator, Group: lead.Group, GroupGeneration: lead.GroupGeneration,
		Member: lead.Member, MemberGeneration: lead.Generation, HubHost: host, HubPort: p,
		InboundToken: testToken, SendWait: 5 * time.Second,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	sent, err := c.Send(ctx, SendRequest{Peer: testWorker, Text: "hang", Wait: dur(0)}, nil)
	if err != nil || sent.IsError() || sent.Handle == "" {
		t.Fatalf("send = %+v, %v", sent, err)
	}
	waitState(t, func() string {
		res, _ := c.Get(ctx, sent.Handle, dur(0), nil)
		if res.IsError() {
			t.Fatalf("get = %+v", res)
		}
		return res.State
	}, a2a.TaskStateWorking)

	res, err := c.Cancel(ctx, sent.Handle)
	if err != nil || res.IsError() || res.State != string(a2a.TaskStateCanceled) || res.Termination != string(a2a.TerminationConfirmed) {
		t.Fatalf("cancel = %+v, %v", res, err)
	}
	res, err = c.Get(ctx, sent.Handle, dur(0), nil)
	if err != nil || res.IsError() || res.State != string(a2a.TaskStateCanceled) {
		t.Fatalf("get after cancel = %+v, %v", res, err)
	}

	// The adapter answers any known task ID, so the client must refuse a foreign generation (I2 B9).
	h, _ := a2a.ParseTaskHandle(sent.Handle)
	h.Generation = "0000000000000000"
	for name, call := range map[string]func() (Result, error){
		"get":    func() (Result, error) { return c.Get(ctx, h.String(), dur(0), nil) },
		"cancel": func() (Result, error) { return c.Cancel(ctx, h.String()) },
	} {
		res, err := call()
		if err != nil || !res.IsError() || !strings.Contains(res.Error, "another generation of worker "+testWorker) {
			t.Fatalf("foreign generation %s = %+v, %v", name, res, err)
		}
	}
	res, err = c.Send(ctx, SendRequest{Peer: testWorker, Text: "x", Context: a2a.ContextHandle{Worker: testWorker, Generation: h.Generation, ContextID: "c1"}.String()}, nil)
	if err != nil || !res.IsError() || !strings.Contains(res.Error, "another generation") {
		t.Fatalf("foreign generation context = %+v, %v", res, err)
	}
}

func waitState(t *testing.T, state func() string, want a2a.TaskState) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for got := state(); got != string(want); got = state() {
		if time.Now().After(deadline) {
			t.Fatalf("state = %s, want %s", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
