// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
)

// streamingAdapter writes the events it receives on events, one flush each,
// and ends the stream when events is closed.
func streamingAdapter(events <-chan string, hijackOnClose bool) func(w http.ResponseWriter, r *http.Request, body []byte) {
	return func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", a2a.SSEContentType)
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_ = rc.Flush()
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					if hijackOnClose {
						conn, _, err := rc.Hijack()
						if err == nil {
							_ = conn.Close()
						}
					}
					return
				}
				_, _ = io.WriteString(w, ev)
				_ = rc.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
}

func streamEvent(id int, result string) string {
	return `data: {"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"result":` + result + "}\n\n"
}

func streamBody(id int) string {
	return strings.Replace(sendBody(id, "stream it"), `"SendMessage"`, `"SendStreamingMessage"`, 1)
}

func TestSSEPassthroughIsUnbufferedAndUnchanged(t *testing.T) {
	f := newFixture(t, nil)
	events := make(chan string)
	adapter, _ := newAdapter(t, streamingAdapter(events, false))
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	f.publishAndReload()

	req := f.request(http.MethodPost, "/peers/worker-1", f.tokens["orch"], streamBody(5))
	resp := f.do(req)
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != a2a.SSEContentType {
		t.Fatalf("content type %q", ct)
	}
	sent := []string{
		streamEvent(5, `{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_SUBMITTED"}}}`),
		": keepalive\n\n",
		// Two data lines and odd spacing must pass byte for byte.
		"data: {\"jsonrpc\":\"2.0\",\"id\":5,\ndata: \"result\":{\"statusUpdate\":{\"taskId\":\"t1\",\"contextId\":\"c1\",\"status\":{\"state\":\"TASK_STATE_WORKING\"}}}}\n\n",
		streamEvent(5, `{"statusUpdate":{"taskId":"t1","contextId":"c1","status":{"state":"TASK_STATE_COMPLETED"},"metadata":{"`+a2a.ExtensionURI+`":{"termination":"confirmed"}}}}`),
	}
	var got strings.Builder
	buf := make([]byte, 64<<10)
	for _, ev := range sent {
		events <- ev
		// Each event must arrive before the adapter sends the next one.
		want := got.Len() + len(ev)
		deadline := time.Now().Add(5 * time.Second)
		for got.Len() < want {
			if time.Now().After(deadline) {
				t.Fatalf("event not relayed promptly: %q", ev)
			}
			n, err := resp.Body.Read(buf)
			got.Write(buf[:n])
			if err != nil {
				t.Fatalf("read: %v", err)
			}
		}
	}
	close(events)
	rest, _ := io.ReadAll(resp.Body)
	got.Write(rest)
	if got.String() != strings.Join(sent, "") {
		t.Fatalf("stream altered:\n%q\nwant\n%q", got.String(), strings.Join(sent, ""))
	}
	if s, ok := f.proxy.cache.summary("worker-1", "t1"); !ok || s.state != a2a.TaskStateCompleted || s.termination != a2a.TerminationConfirmed {
		t.Fatalf("cache summary %+v %v", s, ok)
	}
}

func TestStreamAdapterLossEndsWithErrorEvent(t *testing.T) {
	f := newFixture(t, nil)
	events := make(chan string, 1)
	adapter, _ := newAdapter(t, streamingAdapter(events, true))
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	f.publishAndReload()

	s := openStream(t, f, f.request(http.MethodPost, "/peers/worker-1", f.tokens["orch"], streamBody(11)))
	events <- streamEvent(11, `{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}}`)
	s.next(t)
	// A partial event, then the connection drops.
	events <- "data: {\"jsonrpc\""
	close(events)
	ev := s.next(t)
	var out a2a.Response
	if err := json.Unmarshal(ev.Data, &out); err != nil {
		t.Fatalf("final event %q: %v", ev.Data, err)
	}
	wantCode(t, out, a2a.CodeDeliveryAdapterUnreachable, a2a.DeliveryAdapterUnreachable)
	if string(out.ID) != "11" {
		t.Fatalf("id %s", out.ID)
	}
	s.expectEOF(t)
}

func TestReloadClosesStreamsOfRevokedPrincipals(t *testing.T) {
	f := newFixture(t, nil)
	events := make(chan string, 1)
	adapter, _ := newAdapter(t, streamingAdapter(events, false))
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	f.publishAndReload()

	rpc := openStream(t, f, f.request(http.MethodPost, "/peers/worker-1", f.tokens["orch"], streamBody(3)))
	events <- streamEvent(3, `{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}}`)
	rpc.next(t)
	live := openStream(t, f, f.request(http.MethodGet, a2a.HubLivenessPath, f.tokens["orch"], ""))
	live.next(t)

	f.remove("orch")
	rev := f.publishAndReload()
	if f.status().AppliedRevision != rev {
		t.Fatal("revocation not acknowledged")
	}
	ev := rpc.next(t)
	var out a2a.Response
	if err := json.Unmarshal(ev.Data, &out); err != nil {
		t.Fatal(err)
	}
	wantCode(t, out, a2a.CodeDeliveryRevoked, a2a.DeliveryRevoked)
	rpc.expectEOF(t)
	live.expectEOF(t)
}

func TestReloadClosesStreamsToRemovedWorker(t *testing.T) {
	f := newFixture(t, nil)
	events := make(chan string, 1)
	adapter, _ := newAdapter(t, streamingAdapter(events, false))
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	f.publishAndReload()

	rpc := openStream(t, f, f.request(http.MethodPost, "/peers/worker-1", f.tokens["orch"], streamBody(4)))
	events <- streamEvent(4, `{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}}`)
	rpc.next(t)
	control := openStream(t, f, f.request(http.MethodGet, a2a.HubControlPath, f.tokens["worker-1"], ""))
	control.next(t) // hello
	control.next(t) // orchestrator state

	f.remove("worker-1")
	f.publishAndReload()
	ev := rpc.next(t)
	var out a2a.Response
	if err := json.Unmarshal(ev.Data, &out); err != nil {
		t.Fatal(err)
	}
	wantCode(t, out, a2a.CodeDeliveryRevoked, a2a.DeliveryRevoked)
	if info, _ := out.Error.Info(); info.Metadata["member"] != "worker-1" {
		t.Fatalf("metadata %+v", info.Metadata)
	}
	rpc.expectEOF(t)
	control.expectEOF(t)
}

func TestHostStreamEndsWhenCredentialExpires(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Now = time.Now })
	events := make(chan string, 1)
	adapter, _ := newAdapter(t, streamingAdapter(events, false))
	f.addWorker("worker-1", adapter)
	f.now = time.Now()
	cred := f.addHostCredential(1500 * time.Millisecond)
	f.publishAndReload()
	s := openStream(t, f, f.request(http.MethodPost, "/peers/worker-1", cred, streamBody(1)))
	events <- streamEvent(1, `{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}}`)
	s.next(t)
	ev := s.next(t)
	var out a2a.Response
	if err := json.Unmarshal(ev.Data, &out); err != nil {
		t.Fatal(err)
	}
	wantCode(t, out, a2a.CodeDeliveryRevoked, a2a.DeliveryRevoked)
	s.expectEOF(t)
}

func TestControlStreamReportsOrchestratorLiveness(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.KeepaliveInterval = 50 * time.Millisecond })
	adapter, _ := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	rev := f.publishAndReload()

	control := openStream(t, f, f.request(http.MethodGet, a2a.HubControlPath, f.tokens["worker-1"], ""))
	if ct := control.resp.Header.Get("Content-Type"); ct != a2a.SSEContentType {
		t.Fatalf("content type %q", ct)
	}
	ev := control.next(t)
	var hello a2a.ControlHello
	if ev.Event != a2a.ControlEventHello || json.Unmarshal(ev.Data, &hello) != nil {
		t.Fatalf("first event %+v", ev)
	}
	if hello.Epoch != f.proxy.Epoch() || hello.Revision != uint64(rev) || hello.GroupGeneration != string(testGroupGen) {
		t.Fatalf("hello %+v", hello)
	}
	state := func() a2a.OrchestratorState {
		t.Helper()
		ev := control.next(t)
		var s a2a.OrchestratorState
		if ev.Event != a2a.ControlEventOrchestrator || json.Unmarshal(ev.Data, &s) != nil {
			t.Fatalf("event %+v", ev)
		}
		return s
	}
	if s := state(); s.Connected || s.Member != "orch" || s.Generation != string(f.generation("orch")) {
		t.Fatalf("initial state %+v", s)
	}
	live := openStream(t, f, f.request(http.MethodGet, a2a.HubLivenessPath, f.tokens["orch"], ""))
	if ev := live.next(t); ev.Event != a2a.ControlEventHello {
		t.Fatalf("liveness first event %+v", ev)
	}
	if s := state(); !s.Connected || s.Member != "orch" {
		t.Fatalf("connected state %+v", s)
	}
	_ = live.resp.Body.Close()
	if s := state(); s.Connected {
		t.Fatalf("disconnected state %+v", s)
	}
}

func TestControlStreamReportsMembership(t *testing.T) {
	f := newFixture(t, nil)
	adapter, _ := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
	f.addWorker("worker-1", adapter)
	f.addOrchestrator("orch")
	rev := f.publishAndReload()
	want := func() []a2a.MemberPrincipal {
		out := []a2a.MemberPrincipal{}
		for _, name := range []string{"orch", "worker-1", "worker-2"} {
			for _, m := range f.members {
				if m.Name == name {
					out = append(out, a2a.MemberPrincipal{Member: m.Name, Role: m.Role, Generation: string(m.Generation)})
				}
			}
		}
		return append(out, a2a.MemberPrincipal{Member: "_host", Role: a2a.RoleHost, Generation: string(testHostGen)})
	}

	control := openStream(t, f, f.request(http.MethodGet, a2a.HubControlPath, f.tokens["worker-1"], ""))
	var hello a2a.ControlHello
	if ev := control.next(t); ev.Event != a2a.ControlEventHello || json.Unmarshal(ev.Data, &hello) != nil {
		t.Fatalf("first event %+v", ev)
	}
	if !slices.Equal(hello.Principals, want()) {
		t.Fatalf("hello principals %+v, want %+v", hello.Principals, want())
	}
	control.next(t) // orchestrator state

	other, _ := newAdapter(t, taskResult("t2", a2a.TaskStateCompleted))
	f.addWorker("worker-2", other)
	next := f.publishAndReload()
	var m a2a.ControlMembership
	if ev := control.next(t); ev.Event != a2a.ControlEventMembership || json.Unmarshal(ev.Data, &m) != nil {
		t.Fatalf("membership event %+v", ev)
	}
	if m.Revision != uint64(next) || next == rev || !slices.Equal(m.Principals, want()) {
		t.Fatalf("membership %+v, want revision %d and %+v", m, next, want())
	}
}

func TestKeepaliveComments(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.KeepaliveInterval = 20 * time.Millisecond })
	f.addOrchestrator("orch")
	f.publishAndReload()
	resp := f.do(f.request(http.MethodGet, a2a.HubLivenessPath, f.tokens["orch"], ""))
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 4096)
	var got strings.Builder
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(got.String(), ": keepalive\n\n") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("no keepalives: %q", got.String())
		}
		n, err := resp.Body.Read(buf)
		got.Write(buf[:n])
		if err != nil {
			t.Fatal(err)
		}
	}
}
