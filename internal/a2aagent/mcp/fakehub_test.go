// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
)

const (
	testToken   = "inbound-token"
	testWorker  = "builder"
	testWorkerG = "0123456789abcdef"
)

// fakeHub is a hub plus one serial worker, enough for the client's view of section 11.
type fakeHub struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	tasks    map[string]*fakeTask
	order    []string
	byMsg    map[string]string
	contexts map[string]bool
	changed  chan struct{}
	nextID   int

	// Knobs.
	sendError      *a2a.Error
	dropAfterFirst bool
	abortSend      bool
	completeOnDrop string
	// unconfirmed answers the next n requests of a method with -31019 before looking at them.
	unconfirmed map[string]int

	// Observations.
	sends        []a2a.Message
	refused      []a2a.Message
	cancels      []string
	methods      []string
	liveConnects int
	liveHold     chan struct{}
	liveSilent   bool
}

type fakeTask struct {
	id, contextID, messageID, text string
	state                          a2a.TaskState
	result                         string
	reason                         a2a.EndingReason
	termination                    a2a.Termination
}

func newFakeHub(t *testing.T) *fakeHub {
	h := &fakeHub{
		t:           t,
		tasks:       map[string]*fakeTask{},
		byMsg:       map[string]string{},
		contexts:    map[string]bool{},
		unconfirmed: map[string]int{},
		changed:     make(chan struct{}),
		liveHold:    make(chan struct{}),
	}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(func() {
		h.mu.Lock()
		select {
		case <-h.liveHold:
		default:
			close(h.liveHold)
		}
		h.mu.Unlock()
		h.srv.CloseClientConnections()
		h.srv.Close()
	})
	return h
}

func (h *fakeHub) config() a2aagent.Config {
	host, port, err := net.SplitHostPort(strings.TrimPrefix(h.srv.URL, "http://"))
	if err != nil {
		h.t.Fatal(err)
	}
	p, _ := strconv.Atoi(port)
	return a2aagent.Config{
		Role:             a2a.RoleOrchestrator,
		Group:            "review",
		GroupGeneration:  "aaaaaaaaaaaaaaaa",
		Member:           "lead",
		MemberGeneration: "bbbbbbbbbbbbbbbb",
		HubHost:          host,
		HubPort:          p,
		InboundToken:     testToken,
	}
}

// notify wakes streams; callers hold h.mu.
func (h *fakeHub) notify() {
	close(h.changed)
	h.changed = make(chan struct{})
}

func (h *fakeHub) set(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn()
}

func (h *fakeHub) finish(taskID, result string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	task := h.tasks[taskID]
	task.state = a2a.TaskStateCompleted
	task.result = result
	h.notify()
}

func (h *fakeHub) start(taskID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tasks[taskID].state = a2a.TaskStateWorking
	h.notify()
}

func (h *fakeHub) waitTasks(n int) []string {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		if len(h.order) >= n {
			ids := append([]string(nil), h.order...)
			h.mu.Unlock()
			return ids
		}
		h.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %d tasks", n)
	return nil
}

func (h *fakeHub) cancelCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.cancels)
}

func (h *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(a2a.HeaderAuthorization) != a2a.BearerPrefix+testToken {
		writeRPC(w, nil, nil, a2a.NewError(a2a.CodeDeliveryUnauthenticated, "unauthenticated", "UNAUTHENTICATED", nil))
		return
	}
	if e := a2a.CheckVersion(r.Header); e != nil {
		writeRPC(w, nil, nil, e)
		return
	}
	if e := a2a.CheckExtensions(r.Header); e != nil {
		writeRPC(w, nil, nil, e)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == a2a.HubPeersPath:
		_ = json.NewEncoder(w).Encode(a2a.PeerListing{
			Group: "review", Peers: []a2a.PeerEntry{
				{Name: testWorker, Generation: testWorkerG, Description: "builds things"},
				{Name: "tester", Generation: "fedcba9876543210"},
			},
		})
	case r.Method == http.MethodGet && r.URL.Path == a2a.HubLivenessPath:
		h.serveLiveness(w, r)
	case r.Method == http.MethodPost && r.URL.Path == a2a.PeerPathPrefix+testWorker:
		h.serveRPC(w, r)
	default:
		writeRPC(w, nil, nil, a2a.NewError(a2a.CodeDeliveryUnknownMember, "unknown member", a2a.DeliveryUnknownMember, nil))
	}
}

func (h *fakeHub) serveLiveness(w http.ResponseWriter, r *http.Request) {
	sse, err := a2a.NewSSEWriter(w)
	if err != nil {
		h.t.Error(err)
		return
	}
	h.mu.Lock()
	h.liveConnects++
	hold, silent := h.liveHold, h.liveSilent
	h.mu.Unlock()
	data, _ := json.Marshal(a2a.ControlHello{Epoch: "e1", Revision: 1})
	_ = sse.WriteEvent(a2a.Event{Event: a2a.ControlEventHello, Data: data})
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hold:
			return
		case <-ticker.C:
			if !silent {
				_ = sse.WriteComment("keepalive")
			}
		}
	}
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *a2a.Error) {
	resp := a2a.Response{JSONRPC: "2.0", ID: id, Error: rpcErr}
	if rpcErr == nil {
		raw, _ := json.Marshal(result)
		resp.Result = raw
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *fakeHub) snapshot(task *fakeTask) a2a.Task {
	out := a2a.Task{ID: task.id, ContextID: task.contextID, Status: a2a.TaskStatus{State: task.state}}
	meta := a2a.EnclaveMeta{Reason: task.reason, Termination: task.termination}
	if task.state == a2a.TaskStateSubmitted {
		pos := 0
		for _, id := range h.order {
			if id == task.id {
				break
			}
			if s := h.tasks[id].state; s == a2a.TaskStateSubmitted || s == a2a.TaskStateWorking {
				pos++
			}
		}
		meta.QueuePosition = &pos
	}
	out.Metadata = a2a.WithEnclaveMeta(nil, meta)
	if task.state.Terminal() && task.result != "" {
		out.Artifacts = []a2a.Artifact{{ArtifactID: "result", Parts: []a2a.Part{a2a.TextPart(task.result)}}}
	}
	return out
}

func (h *fakeHub) serveRPC(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	req, rpcErr := a2a.DecodeRequest(body)
	if rpcErr != nil {
		writeRPC(w, nil, nil, rpcErr)
		return
	}
	h.mu.Lock()
	h.methods = append(h.methods, req.Method)
	refuse := h.unconfirmed[req.Method] > 0
	if refuse {
		h.unconfirmed[req.Method]--
		if req.Method == a2a.MethodSendStreamingMessage {
			var params a2a.SendMessageRequest
			_ = a2a.DecodeParams(req, &params)
			h.refused = append(h.refused, params.Message)
		}
	}
	h.mu.Unlock()
	if refuse {
		writeRPC(w, req.ID, nil, a2a.NewError(a2a.CodeEpochUnconfirmed, "Hub epoch not confirmed", a2a.RefusalEpochUnconfirmed, nil))
		return
	}
	switch req.Method {
	case a2a.MethodSendStreamingMessage:
		var params a2a.SendMessageRequest
		if e := a2a.DecodeParams(req, &params); e != nil {
			writeRPC(w, req.ID, nil, e)
			return
		}
		task, e := h.admit(params.Message)
		if e != nil {
			writeRPC(w, req.ID, nil, e)
			return
		}
		h.mu.Lock()
		abort, drop := h.abortSend, h.dropAfterFirst
		h.abortSend = false
		h.mu.Unlock()
		if abort {
			// Admitted, but the client never sees the task ID.
			panic(http.ErrAbortHandler)
		}
		h.stream(w, r, req.ID, task.id, drop)
	case a2a.MethodSubscribeToTask:
		var params a2a.SubscribeToTaskRequest
		_ = a2a.DecodeParams(req, &params)
		h.mu.Lock()
		task, ok := h.tasks[params.ID]
		terminal := ok && task.state.Terminal()
		h.mu.Unlock()
		switch {
		case !ok:
			writeRPC(w, req.ID, nil, &a2a.Error{Code: a2a.CodeTaskNotFound, Message: "task not found"})
		case terminal:
			writeRPC(w, req.ID, nil, &a2a.Error{Code: a2a.CodeUnsupportedOperation, Message: "task is terminal"})
		default:
			h.stream(w, r, req.ID, params.ID, false)
		}
	case a2a.MethodGetTask, a2a.MethodCancelTask:
		var params a2a.GetTaskRequest
		_ = a2a.DecodeParams(req, &params)
		h.mu.Lock()
		task, ok := h.tasks[params.ID]
		if ok && req.Method == a2a.MethodCancelTask {
			h.cancels = append(h.cancels, params.ID)
			if !task.state.Terminal() {
				task.state, task.reason, task.termination = a2a.TaskStateCanceled, a2a.ReasonCancel, a2a.TerminationConfirmed
				h.notify()
			}
		}
		var snap a2a.Task
		if ok {
			snap = h.snapshot(task)
		}
		h.mu.Unlock()
		if !ok {
			writeRPC(w, req.ID, nil, &a2a.Error{Code: a2a.CodeTaskNotFound, Message: "task not found"})
			return
		}
		// GetTask and CancelTask answer a bare Task, not a StreamResponse.
		writeRPC(w, req.ID, snap, nil)
	default:
		writeRPC(w, req.ID, nil, &a2a.Error{Code: a2a.CodeMethodNotFound, Message: "method not found"})
	}
}

// admit applies the adapter's dedupe rules for message IDs (proposal 10).
func (h *fakeHub) admit(msg a2a.Message) (*fakeTask, *a2a.Error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sends = append(h.sends, msg)
	if h.sendError != nil {
		e := h.sendError
		h.sendError = nil
		return nil, e
	}
	meta, _, _ := a2a.EnclaveMetaFrom(msg.Metadata)
	text := partsText(msg.Parts)
	if id, ok := h.byMsg[msg.MessageID]; ok {
		task := h.tasks[id]
		if !meta.Retry || task.text != text {
			return nil, a2a.NewError(a2a.CodeMessageIDConflict, "message ID reused", "MESSAGE_ID_CONFLICT", nil)
		}
		return task, nil
	}
	if meta.Retry {
		return nil, a2a.NewError(a2a.CodeRetryAmbiguous, "retry ambiguous", "RETRY_AMBIGUOUS", nil)
	}
	h.nextID++
	contextID := msg.ContextID
	if contextID == "" {
		contextID = fmt.Sprintf("c%d", h.nextID)
		h.contexts[contextID] = true
	} else if !h.contexts[contextID] {
		return nil, a2a.NewError(a2a.CodeContextUnknown, "context unknown", "CONTEXT_UNKNOWN", nil)
	}
	task := &fakeTask{
		id: fmt.Sprintf("t%d", h.nextID), contextID: contextID, messageID: msg.MessageID,
		text: text, state: a2a.TaskStateSubmitted,
	}
	h.tasks[task.id] = task
	h.order = append(h.order, task.id)
	h.byMsg[msg.MessageID] = task.id
	h.notify()
	return task, nil
}

// stream writes the task snapshot, then status updates until terminal or the client leaves.
func (h *fakeHub) stream(w http.ResponseWriter, r *http.Request, id json.RawMessage, taskID string, dropAfterFirst bool) {
	sse, err := a2a.NewSSEWriter(w)
	if err != nil {
		h.t.Error(err)
		return
	}
	writeEvent := func(res a2a.Result) {
		raw, _ := json.Marshal(res)
		data, _ := json.Marshal(a2a.Response{JSONRPC: "2.0", ID: id, Result: raw})
		_ = sse.WriteEvent(a2a.Event{Data: data})
	}
	h.mu.Lock()
	snap := h.snapshot(h.tasks[taskID])
	changed := h.changed
	h.mu.Unlock()
	writeEvent(a2a.Result{Task: &snap})
	if dropAfterFirst {
		h.mu.Lock()
		if h.completeOnDrop != "" {
			task := h.tasks[taskID]
			task.state, task.result = a2a.TaskStateCompleted, h.completeOnDrop
			h.notify()
		}
		h.mu.Unlock()
		return
	}
	last := snap.Status.State
	for !last.Terminal() {
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		}
		h.mu.Lock()
		task := h.tasks[taskID]
		snap = h.snapshot(task)
		changed = h.changed
		h.mu.Unlock()
		if snap.Status.State == last && !last.Terminal() {
			continue
		}
		last = snap.Status.State
		if last.Terminal() && len(snap.Artifacts) > 0 {
			writeEvent(a2a.Result{ArtifactUpdate: &a2a.TaskArtifactUpdateEvent{
				TaskID: taskID, ContextID: snap.ContextID, Artifact: snap.Artifacts[0],
			}})
		}
		writeEvent(a2a.Result{StatusUpdate: &a2a.TaskStatusUpdateEvent{
			TaskID: taskID, ContextID: snap.ContextID, Status: snap.Status, Metadata: snap.Metadata,
		}})
	}
}
