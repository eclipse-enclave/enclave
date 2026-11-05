// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
)

func newTestClient(t *testing.T, h *fakeHub, dir string) *Client {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	cfg := h.config()
	cfg.SendWait = 5 * time.Second
	c, err := New(Options{Config: cfg, JournalDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func dur(d time.Duration) *time.Duration { return &d }

func TestNewRejectsWorkerRole(t *testing.T) {
	h := newFakeHub(t)
	cfg := h.config()
	cfg.Role = a2a.RoleWorker
	if _, err := New(Options{Config: cfg, JournalDir: t.TempDir()}); err == nil {
		t.Fatal("worker role accepted")
	}
}

func TestSendReturnsFinalMessage(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	go func() {
		id := h.waitTasks(1)[0]
		h.start(id)
		h.finish(id, "done: 3 files")
	}()
	res, err := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "build it"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError() || res.State != string(a2a.TaskStateCompleted) || res.Message != "done: 3 files" {
		t.Fatalf("result = %+v", res)
	}
	if res.Handle != "task:builder:"+testWorkerG+":t1" || res.Context != "ctx:builder:"+testWorkerG+":c1" {
		t.Fatalf("handles = %q %q", res.Handle, res.Context)
	}
	if res.MessageID == "" || res.ExitCode() != 0 {
		t.Fatalf("messageId %q exit %d", res.MessageID, res.ExitCode())
	}
	entries, _ := c.journal.Entries()
	if len(entries) != 1 || entries[0].Handle != res.Handle || entries[0].State != res.State || entries[0].Attempts != 1 {
		t.Fatalf("journal = %+v", entries)
	}
}

func TestSendWaitElapsedReturnsQueuedHandle(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	first, err := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "one", Wait: dur(0)}, nil)
	if err != nil || first.State != string(a2a.TaskStateSubmitted) {
		t.Fatalf("first = %+v, %v", first, err)
	}
	h.start("t1")
	res, err := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "two", Wait: dur(100 * time.Millisecond)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != string(a2a.TaskStateSubmitted) || res.QueuePosition == nil || *res.QueuePosition != 1 {
		t.Fatalf("result = %+v", res)
	}
	if res.Note != noteWaitElapsed || res.Handle == "" || res.ExitCode() != 4 {
		t.Fatalf("result = %+v", res)
	}
	if h.cancelCount() != 0 {
		t.Fatal("a wait timeout canceled the task")
	}
}

func TestWaitIsClampedToSendWait(t *testing.T) {
	h := newFakeHub(t)
	cfg := h.config()
	cfg.SendWait = 150 * time.Millisecond
	c, err := New(Options{Config: cfg, JournalDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "x", Wait: dur(time.Hour)}, nil)
	if err != nil || res.Note != noteWaitElapsed {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %s", elapsed)
	}
}

func TestSendFallsBackAfterDroppedStream(t *testing.T) {
	h := newFakeHub(t)
	h.set(func() { h.dropAfterFirst = true })
	c := newTestClient(t, h, "")
	go func() {
		id := h.waitTasks(1)[0]
		time.Sleep(50 * time.Millisecond)
		h.finish(id, "late")
	}()
	res, err := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "x"}, nil)
	if err != nil || res.State != string(a2a.TaskStateCompleted) || res.Message != "late" {
		t.Fatalf("result = %+v, %v", res, err)
	}
}

func TestSubscribeOnTerminalFallsBackToGetTask(t *testing.T) {
	h := newFakeHub(t)
	h.set(func() { h.dropAfterFirst, h.completeOnDrop = true, "finished meanwhile" })
	c := newTestClient(t, h, "")
	res, err := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "x"}, nil)
	if err != nil || res.State != string(a2a.TaskStateCompleted) || res.Message != "finished meanwhile" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	h.mu.Lock()
	methods := strings.Join(h.methods, ",")
	h.mu.Unlock()
	if !strings.Contains(methods, a2a.MethodSubscribeToTask+","+a2a.MethodGetTask) {
		t.Fatalf("methods = %s", methods)
	}
}

func TestStopWaitingNeverCancels(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		h.waitTasks(1)
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := c.Send(ctx, SendRequest{Peer: testWorker, Text: "x"}, nil)
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v", err)
	}
	if h.cancelCount() != 0 {
		t.Fatal("stopping the wait canceled the task")
	}
	entries, _ := c.journal.Entries()
	if len(entries) != 1 || entries[0].Handle == "" {
		t.Fatalf("journal lost the handle: %+v", entries)
	}
	res, err := c.Cancel(context.Background(), entries[0].Handle)
	if err != nil || res.State != string(a2a.TaskStateCanceled) || res.Termination != string(a2a.TerminationConfirmed) || res.Reason != string(a2a.ReasonCancel) {
		t.Fatalf("cancel = %+v, %v", res, err)
	}
	if res.MessageID != entries[0].MessageID || res.ExitCode() != 5 {
		t.Fatalf("cancel = %+v", res)
	}
}

func TestRetryAfterClientRestartReusesMessageID(t *testing.T) {
	h := newFakeHub(t)
	dir := t.TempDir()
	h.set(func() { h.abortSend = true })
	first := newTestClient(t, h, dir)
	res, err := first.Send(context.Background(), SendRequest{Peer: testWorker, Text: "deploy", Wait: dur(time.Second)}, nil)
	if err != nil || !res.IsError() || res.MessageID == "" || !strings.Contains(res.Note, "retry") {
		t.Fatalf("lost send = %+v, %v", res, err)
	}
	original := res.MessageID

	// A new process, as after Claude Code respawns the stdio server.
	second := newTestClient(t, h, dir)
	go func() {
		h.waitTasks(1)
		h.finish("t1", "deployed")
	}()
	res, err = second.Send(context.Background(), SendRequest{Peer: testWorker, Text: "deploy", Retry: true}, nil)
	if err != nil || res.State != string(a2a.TaskStateCompleted) || res.MessageID != original {
		t.Fatalf("retry = %+v, %v", res, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.order) != 1 || len(h.sends) != 2 {
		t.Fatalf("tasks %d sends %d", len(h.order), len(h.sends))
	}
	retryMeta, _, _ := a2a.EnclaveMetaFrom(h.sends[1].Metadata)
	if h.sends[1].MessageID != original || !retryMeta.Retry {
		t.Fatalf("retry message = %+v", h.sends[1])
	}
	if meta, ok, _ := a2a.EnclaveMetaFrom(h.sends[0].Metadata); ok && meta.Retry {
		t.Fatal("first send flagged as retry")
	}
}

func TestRetryResolution(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	ctx := context.Background()

	res, _ := c.Send(ctx, SendRequest{Peer: testWorker, Text: "never sent", Retry: true}, nil)
	if !res.IsError() || !strings.Contains(res.Error, "no earlier send") {
		t.Fatalf("no match = %+v", res)
	}
	res, _ = c.Send(ctx, SendRequest{Peer: testWorker, Text: "x", MessageID: "m1"}, nil)
	if !res.IsError() || !strings.Contains(res.Error, "retry: true") {
		t.Fatalf("message_id without retry = %+v", res)
	}

	a, _ := c.Send(ctx, SendRequest{Peer: testWorker, Text: "same", Wait: dur(0)}, nil)
	b, _ := c.Send(ctx, SendRequest{Peer: testWorker, Text: "same", Wait: dur(0)}, nil)
	if a.MessageID == b.MessageID {
		t.Fatal("unflagged sends shared a message ID")
	}
	res, _ = c.Send(ctx, SendRequest{Peer: testWorker, Text: "same", Retry: true}, nil)
	if !res.IsError() || len(res.Candidates) != 2 {
		t.Fatalf("ambiguous = %+v", res)
	}
	res, _ = c.Send(ctx, SendRequest{Peer: testWorker, Text: "other", Retry: true, MessageID: a.MessageID}, nil)
	if !res.IsError() || len(res.Candidates) != 1 {
		t.Fatalf("mismatched message_id = %+v", res)
	}
	res, _ = c.Send(ctx, SendRequest{Peer: testWorker, Text: "same", Retry: true, MessageID: a.MessageID, Wait: dur(0)}, nil)
	if res.IsError() || res.Handle != a.Handle {
		t.Fatalf("explicit retry = %+v, original %+v", res, a)
	}
	entries, _ := c.journal.Entries()
	last := entries[len(entries)-1]
	if last.MessageID != a.MessageID || last.Attempts != 2 {
		t.Fatalf("journal = %+v", entries)
	}
}

func TestRetryUnknownToWorkerIsAmbiguous(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	res, _ := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "x", Retry: true, MessageID: "evicted"}, nil)
	if !res.IsError() || !strings.Contains(res.Error, "RETRY_AMBIGUOUS") || res.MessageID != "evicted" {
		t.Fatalf("result = %+v", res)
	}
}

func TestSendErrors(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	ctx := context.Background()
	h.set(func() { h.sendError = a2a.NewError(a2a.CodeQueueFull, "queue full", "QUEUE_FULL", nil) })
	res, _ := c.Send(ctx, SendRequest{Peer: testWorker, Text: "x"}, nil)
	if !res.IsError() || res.Error != "queue full [-31016 QUEUE_FULL]" || res.ExitCode() != 1 {
		t.Fatalf("queue full = %+v", res)
	}
	res, _ = c.Send(ctx, SendRequest{Peer: "nobody", Text: "x"}, nil)
	if !res.IsError() || !strings.Contains(res.Error, "builder, tester") {
		t.Fatalf("unknown peer = %+v", res)
	}
	res, _ = c.Send(ctx, SendRequest{Peer: testWorker, Text: "x", Context: "ctx:tester:fedcba9876543210:c9"}, nil)
	if !res.IsError() || !strings.Contains(res.Error, "belongs to peer tester") {
		t.Fatalf("foreign context = %+v", res)
	}
	res, _ = c.Send(ctx, SendRequest{Peer: testWorker, Text: "x", Context: "ctx:builder:" + testWorkerG + ":nope"}, nil)
	if !res.IsError() || !strings.Contains(res.Error, "CONTEXT_UNKNOWN") {
		t.Fatalf("unknown context = %+v", res)
	}
	c.maxRequestBytes = 4
	res, _ = c.Send(ctx, SendRequest{Peer: testWorker, Text: "too long"}, nil)
	if !res.IsError() || !strings.Contains(res.Error, "limit") {
		t.Fatalf("too large = %+v", res)
	}
}

func TestContextContinues(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	first, _ := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "a", Wait: dur(0)}, nil)
	second, _ := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "b", Context: first.Context, Wait: dur(0)}, nil)
	if second.IsError() || second.Context != first.Context || second.Handle == first.Handle {
		t.Fatalf("first %+v second %+v", first, second)
	}
}

func TestGetTask(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	ctx := context.Background()

	res, _ := c.Get(ctx, "", nil, nil)
	if res.IsError() || len(res.Recent) != 0 || res.Note == "" {
		t.Fatalf("empty recent = %+v", res)
	}
	res, _ = c.Get(ctx, "b7c1e2a0-claude-task", nil, nil)
	if !res.IsError() || !strings.Contains(res.Error, "not Claude task IDs") {
		t.Fatalf("non-handle = %+v", res)
	}

	sent, _ := c.Send(ctx, SendRequest{Peer: testWorker, Text: "x", Wait: dur(0)}, nil)
	res, _ = c.Get(ctx, sent.Handle, dur(0), nil)
	if res.State != string(a2a.TaskStateSubmitted) || res.MessageID != sent.MessageID || res.Note != "" {
		t.Fatalf("immediate get = %+v", res)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.finish("t1", "ok")
	}()
	res, _ = c.Get(ctx, sent.Handle, nil, nil)
	if res.State != string(a2a.TaskStateCompleted) || res.Message != "ok" {
		t.Fatalf("blocking get = %+v", res)
	}
	res, _ = c.Get(ctx, "", nil, nil)
	if len(res.Recent) != 1 || res.Recent[0].Handle != sent.Handle || res.Recent[0].State != string(a2a.TaskStateCompleted) {
		t.Fatalf("recent = %+v", res)
	}
	res, _ = c.Get(ctx, "task:builder:"+testWorkerG+":missing", dur(0), nil)
	if !res.IsError() || !strings.Contains(res.Note, "lists recent") {
		t.Fatalf("unknown task = %+v", res)
	}
}

func TestListPeers(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	res, err := c.ListPeers(context.Background())
	if err != nil || len(res.Peers) != 2 || res.Peers[0] != (Peer{Name: testWorker, Description: "builds things"}) {
		t.Fatalf("peers = %+v, %v", res, err)
	}
}

func TestProgressAtMostEveryInterval(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	c.progressInterval = 50 * time.Millisecond
	var lines []string
	res, _ := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "x", Wait: dur(275 * time.Millisecond)},
		func(m string) { lines = append(lines, m) })
	if res.Note != noteWaitElapsed {
		t.Fatalf("result = %+v", res)
	}
	if len(lines) < 3 || len(lines) > 5 || lines[0] != "builder: queued, position 0" {
		t.Fatalf("progress = %q", lines)
	}
}

func TestLivenessReconnects(t *testing.T) {
	h := newFakeHub(t)
	h.set(func() { h.liveSilent = true })
	c := newTestClient(t, h, "")
	c.live.idle = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.HoldLiveness(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		n := h.liveConnects
		h.mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("liveness connects = %d, want a reconnect after missed keepalives", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if c.LivenessConnected() {
		t.Fatal("still connected after stop")
	}
}

func TestSendResendsWhileEpochUnconfirmed(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	c.retryBackoff = 10 * time.Millisecond
	h.set(func() { h.unconfirmed[a2a.MethodSendStreamingMessage] = 2 })
	go func() {
		id := h.waitTasks(1)[0]
		h.finish(id, "done")
	}()
	res, err := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "build it"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError() || res.State != string(a2a.TaskStateCompleted) || res.Handle == "" {
		t.Fatalf("result = %+v", res)
	}
	h.mu.Lock()
	refused, sends := append([]a2a.Message(nil), h.refused...), append([]a2a.Message(nil), h.sends...)
	h.mu.Unlock()
	if len(refused) != 2 || len(sends) != 1 {
		t.Fatalf("refused %d, admitted %d sends", len(refused), len(sends))
	}
	for _, m := range append(refused, sends...) {
		meta, _, _ := a2a.EnclaveMetaFrom(m.Metadata)
		if m.MessageID != res.MessageID || meta.Retry {
			t.Fatalf("resend %+v changed the message ID or set retry", m)
		}
	}
	entries, _ := c.journal.Entries()
	if len(entries) != 1 || entries[0].Handle != res.Handle || entries[0].Attempts != 1 {
		t.Fatalf("journal = %+v", entries)
	}
}

func TestSendGivesUpWhenEpochStaysUnconfirmed(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	c.retryBackoff = 10 * time.Millisecond
	h.set(func() { h.unconfirmed[a2a.MethodSendStreamingMessage] = 1 << 20 })
	start := time.Now()
	res, err := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "build it", Wait: dur(150 * time.Millisecond)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("gave up after %s, past the wait", elapsed)
	}
	if !res.IsError() || res.Handle != "" || res.MessageID == "" || res.Note != noteUnconfirmed ||
		!strings.Contains(res.Error, a2a.RefusalEpochUnconfirmed) || res.ExitCode() != 1 {
		t.Fatalf("result = %+v", res)
	}
	h.mu.Lock()
	refused, admitted := len(h.refused), len(h.sends)
	h.mu.Unlock()
	if refused < 2 || admitted != 0 {
		t.Fatalf("refused %d, admitted %d; want several resends and no admission", refused, admitted)
	}
	entries, _ := c.journal.Entries()
	if len(entries) != 1 || entries[0].Handle != "" || entries[0].State != "" || entries[0].Error == "" {
		t.Fatalf("journal marks the send admitted: %+v", entries)
	}
}

func TestGetAndCancelResendWhileEpochUnconfirmed(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	c.retryBackoff = 10 * time.Millisecond
	sent, err := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "x", Wait: dur(0)}, nil)
	if err != nil || sent.Handle == "" {
		t.Fatalf("send = %+v, %v", sent, err)
	}

	h.set(func() { h.unconfirmed[a2a.MethodGetTask] = 2 })
	got, err := c.Get(context.Background(), sent.Handle, dur(time.Second), nil)
	if err != nil || got.IsError() || got.Handle != sent.Handle {
		t.Fatalf("get = %+v, %v", got, err)
	}

	// Without a wait there is no room for a resend.
	h.set(func() { h.unconfirmed[a2a.MethodGetTask] = 1 })
	got, err = c.Get(context.Background(), sent.Handle, dur(0), nil)
	if err != nil || !got.IsError() || got.Handle != sent.Handle || got.Note != noteUnconfirmedTask {
		t.Fatalf("get without wait = %+v, %v", got, err)
	}

	h.set(func() { h.unconfirmed[a2a.MethodCancelTask] = 2 })
	canceled, err := c.Cancel(context.Background(), sent.Handle)
	if err != nil || canceled.State != string(a2a.TaskStateCanceled) || h.cancelCount() != 1 {
		t.Fatalf("cancel = %+v, %v", canceled, err)
	}
}

func TestWaitResubscribesWhileEpochUnconfirmed(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	sent, err := c.Send(context.Background(), SendRequest{Peer: testWorker, Text: "x", Wait: dur(0)}, nil)
	if err != nil || sent.Handle == "" {
		t.Fatalf("send = %+v, %v", sent, err)
	}
	h.set(func() { h.unconfirmed[a2a.MethodSubscribeToTask] = 1 })
	go func() {
		time.Sleep(200 * time.Millisecond)
		h.finish("t1", "done")
	}()
	got, err := c.Get(context.Background(), sent.Handle, dur(3*time.Second), nil)
	if err != nil || got.State != string(a2a.TaskStateCompleted) || got.Message != "done" {
		t.Fatalf("get = %+v, %v", got, err)
	}
}
