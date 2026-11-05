// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/backend"
	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/peergroup"
)

func newA2ARequest(verb string, detach bool, wait *time.Duration) *cli.A2ARequest {
	return &cli.A2ARequest{Verb: verb, Detach: detach, Wait: wait}
}

// fakeHubCall is one scripted hub answer: frames are written in order; hold
// keeps the stream open until the call ends, like a running task.
type fakeHubCall struct {
	frames []string
	hold   bool
}

type fakeHostAccess struct {
	mu       sync.Mutex
	issued   []time.Duration
	revoked  []string
	requests []a2a.Request
	answer   func(req a2a.Request, n int) fakeHubCall
}

func (f *fakeHostAccess) Issue(_ context.Context, _ string, lifetime time.Duration) (peergroup.InvocationCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issued = append(f.issued, lifetime)
	return peergroup.InvocationCredential{Token: "tok", Hash: "sha256:tok"}, nil
}

func (f *fakeHostAccess) Revoke(_ context.Context, _ string, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, hash)
	return nil
}

func (f *fakeHostAccess) Call(ctx context.Context, _ string, path, credential string, body []byte) (io.ReadCloser, error) {
	if credential != "tok" || !strings.HasPrefix(path, a2a.PeerPathPrefix) {
		return nil, fmt.Errorf("bad call %s", path)
	}
	var req a2a.Request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.requests = append(f.requests, req)
	n := len(f.requests)
	f.mu.Unlock()
	answer := f.answer(req, n)
	r, w := io.Pipe()
	go func() {
		for _, frame := range answer.frames {
			if _, err := io.WriteString(w, frame); err != nil {
				return
			}
		}
		if answer.hold {
			<-ctx.Done()
		}
		_ = w.Close()
	}()
	return r, nil
}

func (f *fakeHostAccess) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		out = append(out, r.Method)
	}
	return out
}

func a2aTask(id, state string, meta *a2a.EnclaveMeta, result string) a2a.Result {
	task := &a2a.Task{ID: id, ContextID: "c1", Status: a2a.TaskStatus{State: a2a.TaskState(state)}}
	if meta != nil {
		task.Metadata = a2a.WithEnclaveMeta(nil, *meta)
	}
	if result != "" {
		task.Artifacts = []a2a.Artifact{{ArtifactID: "result", Parts: []a2a.Part{a2a.TextPart(result)}}}
	}
	return a2a.Result{Task: task}
}

func a2aRespJSON(t *testing.T, id json.RawMessage, res *a2a.Result, rpcErr *a2a.Error) string {
	t.Helper()
	resp := a2a.Response{JSONRPC: "2.0", ID: id, Error: rpcErr}
	if res != nil {
		raw, err := a2a.EncodeResult(*res)
		if err != nil {
			t.Fatal(err)
		}
		resp.Result = raw
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// a2aBareTaskJSON answers GetTask and CancelTask, whose result is a bare Task.
func a2aBareTaskJSON(t *testing.T, id json.RawMessage, res a2a.Result) string {
	t.Helper()
	raw, err := json.Marshal(res.Task)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(a2a.Response{JSONRPC: "2.0", ID: id, Result: raw})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func sse(s string) string { return "data: " + s + "\n\n" }

// runHostVerb runs one verb against the fixture group with a fake hub and
// returns exit code and stdout.
func runHostVerb(t *testing.T, f *a2aFixture, access a2aHostAccess, req *cli.A2ARequest, opts model.Options) (int, string) {
	t.Helper()
	var out bytes.Buffer
	oldStdout, oldAccess, oldBackoff := a2aStdout, a2aNewHostAccess, a2aRetryBackoff
	a2aStdout = &out
	a2aRetryBackoff = 10 * time.Millisecond
	a2aNewHostAccess = func(context.Context, model.Options, a2aState) (a2aHostAccess, error) { return access, nil }
	t.Cleanup(func() { a2aStdout, a2aNewHostAccess, a2aRetryBackoff = oldStdout, oldAccess, oldBackoff })
	code := runA2AHostVerb(context.Background(), opts, req, f.state, "review")
	return code, out.String()
}

func a2aTaskJSON(t *testing.T, out string) a2aTaskOutput {
	t.Helper()
	var got a2aTaskOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return got
}

func TestA2AHostSendCompletes(t *testing.T) {
	f := newA2AFixture(t)
	access := &fakeHostAccess{}
	access.answer = func(req a2a.Request, _ int) fakeHubCall {
		working := a2aTask("t1", string(a2a.TaskStateWorking), nil, "")
		done := a2aTask("t1", string(a2a.TaskStateCompleted), &a2a.EnclaveMeta{Termination: a2a.TerminationConfirmed}, "LGTM")
		return fakeHubCall{frames: []string{sse(a2aRespJSON(t, req.ID, &working, nil)), sse(a2aRespJSON(t, req.ID, &done, nil))}}
	}
	req := &cli.A2ARequest{Verb: cli.A2AVerbSend, Peer: "worker-1", Text: "review the diff", JSON: true}
	code, out := runHostVerb(t, f, access, req, config.DefaultOptions())
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	got := a2aTaskJSON(t, out)
	if got.Handle != "task:worker-1:"+string(a2aTestWorkerGen)+":t1" || got.Message != "LGTM" || got.Context != "ctx:worker-1:"+string(a2aTestWorkerGen)+":c1" {
		t.Fatalf("result %+v", got)
	}
	if len(access.issued) != 1 || access.issued[0] != 11*time.Minute || len(access.revoked) != 1 {
		t.Fatalf("credential lifecycle: issued %v revoked %v", access.issued, access.revoked)
	}
	var params a2a.SendMessageRequest
	_ = json.Unmarshal(access.requests[0].Params, &params)
	if access.requests[0].Method != a2a.MethodSendStreamingMessage || params.Message.MessageID != got.MessageID || params.Message.Metadata != nil {
		t.Fatalf("request %+v", access.requests[0])
	}
	j := f.state.readJournal("review", a2aTestGroupGen)
	if len(j.Entries) != 1 || j.Entries[0].Handle != got.Handle || j.Entries[0].Text != "review the diff" || j.Entries[0].State != string(a2a.TaskStateCompleted) {
		t.Fatalf("journal %+v", j)
	}
}

func TestA2AHostSendGolden(t *testing.T) {
	f := newA2AFixture(t)
	access := &fakeHostAccess{}
	access.answer = func(req a2a.Request, _ int) fakeHubCall {
		queued := a2aTask("t9", string(a2a.TaskStateSubmitted), &a2a.EnclaveMeta{QueuePosition: new(int)}, "")
		return fakeHubCall{frames: []string{sse(a2aRespJSON(t, req.ID, &queued, nil))}, hold: true}
	}
	wait := 50 * time.Millisecond
	code, out := runHostVerb(t, f, access, &cli.A2ARequest{Verb: cli.A2AVerbSend, Peer: "worker-1", Text: "x", JSON: true, Wait: &wait}, config.DefaultOptions())
	if code != a2aExitWaitElapsed {
		t.Fatalf("exit %d: %s", code, out)
	}
	got := a2aTaskJSON(t, out)
	got.MessageID = "00000000-0000-4000-8000-000000000000"
	var buf bytes.Buffer
	if err := writeA2AJSON(&buf, got); err != nil {
		t.Fatal(err)
	}
	checkA2AGolden(t, "a2a_task_wait_elapsed.golden.json", buf.Bytes())
}

func TestA2AHostSendOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		answer func(t *testing.T, req a2a.Request, n int) fakeHubCall
		detach bool
		code   int
		check  func(t *testing.T, got a2aTaskOutput, methods []string)
	}{
		{
			name: "failed task exits 5",
			answer: func(t *testing.T, req a2a.Request, _ int) fakeHubCall {
				failed := a2aTask("t1", string(a2a.TaskStateFailed), &a2a.EnclaveMeta{Reason: a2a.ReasonDeadline, Termination: a2a.TerminationConfirmed}, "")
				return fakeHubCall{frames: []string{sse(a2aRespJSON(t, req.ID, &failed, nil))}}
			},
			code: a2aExitTaskNotSuccess,
			check: func(t *testing.T, got a2aTaskOutput, _ []string) {
				if got.Reason != "deadline" || got.Termination != "confirmed" {
					t.Fatalf("%+v", got)
				}
			},
		},
		{
			name: "refusal before admission exits 1",
			answer: func(t *testing.T, req a2a.Request, _ int) fakeHubCall {
				return fakeHubCall{frames: []string{a2aRespJSON(t, req.ID, nil, a2a.NewError(a2a.CodeQueueFull, "queue full", a2a.RefusalQueueFull, nil))}}
			},
			code: 1,
			check: func(t *testing.T, got a2aTaskOutput, _ []string) {
				if !strings.Contains(got.Error, "-31016 QUEUE_FULL") || got.Handle != "" {
					t.Fatalf("%+v", got)
				}
			},
		},
		{
			name: "epoch unconfirmed is resent unchanged",
			answer: func(t *testing.T, req a2a.Request, n int) fakeHubCall {
				if n == 1 {
					return fakeHubCall{frames: []string{a2aRespJSON(t, req.ID, nil, a2a.NewError(a2a.CodeEpochUnconfirmed, "epoch unconfirmed", a2a.RefusalEpochUnconfirmed, nil))}}
				}
				done := a2aTask("t1", string(a2a.TaskStateCompleted), nil, "ok")
				return fakeHubCall{frames: []string{sse(a2aRespJSON(t, req.ID, &done, nil))}}
			},
			code: 0,
			check: func(t *testing.T, got a2aTaskOutput, methods []string) {
				if len(methods) != 2 || methods[1] != a2a.MethodSendStreamingMessage || got.Message != "ok" {
					t.Fatalf("%v %+v", methods, got)
				}
			},
		},
		{
			name: "dropped stream resubscribes, terminal falls back to GetTask",
			answer: func(t *testing.T, req a2a.Request, n int) fakeHubCall {
				switch n {
				case 1:
					working := a2aTask("t1", string(a2a.TaskStateWorking), nil, "")
					return fakeHubCall{frames: []string{sse(a2aRespJSON(t, req.ID, &working, nil))}}
				case 2:
					return fakeHubCall{frames: []string{sse(a2aRespJSON(t, req.ID, nil, &a2a.Error{Code: a2a.CodeUnsupportedOperation, Message: "ended"}))}}
				default:
					done := a2aTask("t1", string(a2a.TaskStateCompleted), nil, "late")
					return fakeHubCall{frames: []string{a2aBareTaskJSON(t, req.ID, done)}}
				}
			},
			code: 0,
			check: func(t *testing.T, got a2aTaskOutput, methods []string) {
				want := []string{a2a.MethodSendStreamingMessage, a2a.MethodSubscribeToTask, a2a.MethodGetTask}
				if strings.Join(methods, ",") != strings.Join(want, ",") || got.Message != "late" {
					t.Fatalf("%v %+v", methods, got)
				}
			},
		},
		{
			name: "credential expiry mid-stream exits 4 with the handle",
			answer: func(t *testing.T, req a2a.Request, _ int) fakeHubCall {
				working := a2aTask("t1", string(a2a.TaskStateWorking), nil, "")
				return fakeHubCall{frames: []string{
					sse(a2aRespJSON(t, req.ID, &working, nil)),
					sse(a2aRespJSON(t, req.ID, nil, a2a.NewError(a2a.CodeDeliveryRevoked, "revoked", a2a.DeliveryRevoked, nil))),
				}}
			},
			code: a2aExitWaitElapsed,
			check: func(t *testing.T, got a2aTaskOutput, _ []string) {
				if got.Handle == "" || got.Error != "" || got.Note != a2aNoteExpired {
					t.Fatalf("%+v", got)
				}
			},
		},
		{
			name: "stream lost before the task ID is an unknown outcome",
			answer: func(t *testing.T, _ a2a.Request, _ int) fakeHubCall {
				return fakeHubCall{}
			},
			code: 1,
			check: func(t *testing.T, got a2aTaskOutput, _ []string) {
				if got.MessageID == "" || got.Note != a2aNoteUnknown {
					t.Fatalf("%+v", got)
				}
			},
		},
		{
			name:   "detach returns once admitted",
			detach: true,
			answer: func(t *testing.T, req a2a.Request, _ int) fakeHubCall {
				var params a2a.SendMessageRequest
				_ = json.Unmarshal(req.Params, &params)
				if req.Method != a2a.MethodSendMessage || params.Configuration == nil || !params.Configuration.ReturnImmediately {
					t.Errorf("detach request %+v", req)
				}
				queued := a2aTask("t1", string(a2a.TaskStateSubmitted), nil, "")
				return fakeHubCall{frames: []string{a2aRespJSON(t, req.ID, &queued, nil)}}
			},
			code: 0,
			check: func(t *testing.T, got a2aTaskOutput, _ []string) {
				if got.State != string(a2a.TaskStateSubmitted) || got.Handle == "" {
					t.Fatalf("%+v", got)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newA2AFixture(t)
			access := &fakeHostAccess{answer: func(req a2a.Request, n int) fakeHubCall { return tc.answer(t, req, n) }}
			code, out := runHostVerb(t, f, access, &cli.A2ARequest{Verb: cli.A2AVerbSend, Peer: "worker-1", Text: "x", JSON: true, Detach: tc.detach}, config.DefaultOptions())
			if code != tc.code {
				t.Fatalf("exit %d, want %d: %s", code, tc.code, out)
			}
			tc.check(t, a2aTaskJSON(t, out), access.methods())
			if len(access.revoked) != 1 {
				t.Fatalf("credential not revoked: %v", access.revoked)
			}
		})
	}
}

func TestA2AHostSendRejectsBeforeIssuing(t *testing.T) {
	f := newA2AFixture(t)
	access := &fakeHostAccess{answer: func(a2a.Request, int) fakeHubCall { return fakeHubCall{} }}
	for _, req := range []*cli.A2ARequest{
		{Verb: cli.A2AVerbSend, Peer: "lead", Text: "x"},
		{Verb: cli.A2AVerbSend, Peer: "nobody", Text: "x"},
		{Verb: cli.A2AVerbSend, Peer: "worker-1", Text: "  "},
		{Verb: cli.A2AVerbSend, Peer: "worker-1", Text: "x", Context: "ctx:worker-2:" + string(a2aTestW2Gen) + ":c1"},
		{Verb: cli.A2AVerbTaskGet, Handle: "not-a-handle"},
		{Verb: cli.A2AVerbTaskRetry, Handle: "task:worker-1:" + string(a2aTestWorkerGen) + ":unknown"},
		{Verb: cli.A2AVerbTaskGet, Handle: "task:worker-1:0000000000000000:t1"},
		{Verb: cli.A2AVerbTaskCancel, Handle: "task:worker-1:0000000000000000:t1"},
		{Verb: cli.A2AVerbTaskGet, Handle: "task:gone:" + string(a2aTestWorkerGen) + ":t1"},
	} {
		if code, out := runHostVerb(t, f, access, req, config.DefaultOptions()); code != 1 {
			t.Fatalf("%+v: exit %d %s", req, code, out)
		}
	}
	if len(access.issued) != 0 {
		t.Fatalf("credential issued for a rejected request: %v", access.issued)
	}
}

func TestA2AHostTaskVerbs(t *testing.T) {
	f := newA2AFixture(t)
	handle := "task:worker-1:" + string(a2aTestWorkerGen) + ":t1"
	access := &fakeHostAccess{}
	access.answer = func(req a2a.Request, _ int) fakeHubCall {
		switch req.Method {
		case a2a.MethodGetTask:
			working := a2aTask("t1", string(a2a.TaskStateWorking), nil, "")
			return fakeHubCall{frames: []string{a2aBareTaskJSON(t, req.ID, working)}}
		case a2a.MethodCancelTask:
			canceled := a2aTask("t1", string(a2a.TaskStateCanceled), &a2a.EnclaveMeta{Reason: a2a.ReasonCancel, Termination: a2a.TerminationUnconfirmed}, "")
			return fakeHubCall{frames: []string{a2aBareTaskJSON(t, req.ID, canceled)}}
		}
		return fakeHubCall{}
	}
	code, out := runHostVerb(t, f, access, &cli.A2ARequest{Verb: cli.A2AVerbTaskGet, Handle: handle, JSON: true}, config.DefaultOptions())
	if code != a2aExitWaitElapsed || a2aTaskJSON(t, out).State != string(a2a.TaskStateWorking) {
		t.Fatalf("get: exit %d %s", code, out)
	}
	code, out = runHostVerb(t, f, access, &cli.A2ARequest{Verb: cli.A2AVerbTaskCancel, Handle: handle, JSON: true}, config.DefaultOptions())
	if got := a2aTaskJSON(t, out); code != 0 || got.Termination != "unconfirmed" || got.Reason != "cancel" {
		t.Fatalf("cancel: exit %d %s", code, out)
	}
	if access.issued[0] != 2*time.Minute || access.issued[1] != 2*time.Minute {
		t.Fatalf("non-blocking lifetimes %v", access.issued)
	}
}

// TestA2AHostRefusesForeignWorkerGeneration covers I2 B9.
func TestA2AHostRefusesForeignWorkerGeneration(t *testing.T) {
	f := newA2AFixture(t)
	access := &fakeHostAccess{answer: func(a2a.Request, int) fakeHubCall { return fakeHubCall{} }}
	code, out := runHostVerb(t, f, access, &cli.A2ARequest{Verb: cli.A2AVerbTaskGet, Handle: "task:worker-1:0000000000000000:t1", JSON: true}, config.DefaultOptions())
	got := a2aTaskJSON(t, out)
	if code != 1 || !strings.Contains(got.Error, "another generation of worker worker-1") || !strings.Contains(got.Error, string(a2aTestWorkerGen)) {
		t.Fatalf("exit %d %s", code, out)
	}
	if len(access.issued) != 0 || len(access.requests) != 0 {
		t.Fatalf("a foreign handle reached the hub: issued %v requests %v", access.issued, access.requests)
	}
}

func TestA2AHostRetryResendsOriginalMessage(t *testing.T) {
	f := newA2AFixture(t)
	first := &fakeHostAccess{answer: func(a2a.Request, int) fakeHubCall { return fakeHubCall{} }}
	code, out := runHostVerb(t, f, first, &cli.A2ARequest{Verb: cli.A2AVerbSend, Peer: "worker-1", Text: "fix it", Context: "ctx:worker-1:" + string(a2aTestWorkerGen) + ":c1", JSON: true}, config.DefaultOptions())
	lost := a2aTaskJSON(t, out)
	if code != 1 || lost.MessageID == "" {
		t.Fatalf("lost send: exit %d %s", code, out)
	}
	retry := &fakeHostAccess{}
	retry.answer = func(req a2a.Request, _ int) fakeHubCall {
		done := a2aTask("t1", string(a2a.TaskStateCompleted), nil, "fixed")
		return fakeHubCall{frames: []string{sse(a2aRespJSON(t, req.ID, &done, nil))}}
	}
	code, out = runHostVerb(t, f, retry, &cli.A2ARequest{Verb: cli.A2AVerbTaskRetry, Handle: lost.MessageID, JSON: true}, config.DefaultOptions())
	if code != 0 {
		t.Fatalf("retry: exit %d %s", code, out)
	}
	var params a2a.SendMessageRequest
	_ = json.Unmarshal(retry.requests[0].Params, &params)
	meta, ok, _ := a2a.EnclaveMetaFrom(params.Message.Metadata)
	if params.Message.MessageID != lost.MessageID || !ok || !meta.Retry || params.Message.ContextID != "c1" || a2aPartsText(params.Message.Parts) != "fix it" {
		t.Fatalf("retry request %+v", params)
	}
	got := a2aTaskJSON(t, out)
	if _, ok := f.state.readJournal("review", a2aTestGroupGen).find(got.Handle); !ok {
		t.Fatal("retry did not record the handle")
	}
}

type fakeHostBackend struct {
	f       *a2aFixture
	revoked []string
	ref     backend.HubRef
	path    string
	stdin   string
}

func (b *fakeHostBackend) IssueHostCredential(_ context.Context, group string, lifetime time.Duration) (string, string, error) {
	rec, err := b.f.state.store.Load(group)
	if err != nil {
		return "", "", err
	}
	rec.Credentials = append(rec.Credentials, peergroup.InvocationRecord{Hash: "sha256:tok", IssuedAt: "2026-11-05T12:00:30Z", ExpiresAt: "2026-11-05T12:02:30Z"})
	return "tok", "sha256:tok", b.f.state.store.Save(rec)
}

func (b *fakeHostBackend) RevokeHostCredential(_ context.Context, _ string, hash string) error {
	b.revoked = append(b.revoked, hash)
	return nil
}

func (b *fakeHostBackend) HubCall(_ context.Context, ref backend.HubRef, path string, stdin io.Reader) (io.ReadCloser, error) {
	data, err := io.ReadAll(stdin)
	b.ref, b.path, b.stdin = ref, path, string(data)
	return io.NopCloser(strings.NewReader("{}")), err
}

func TestEngineHostAccessUsesBackendAPIs(t *testing.T) {
	f := newA2AFixture(t)
	be := &fakeHostBackend{f: f}
	access := &engineA2AHostAccess{creds: be, hub: be, state: f.state}
	cred, err := access.Issue(context.Background(), "review", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if cred.Token != "tok" || cred.Hash != "sha256:tok" || peergroup.FormatTimestamp(cred.ExpiresAt) != "2026-11-05T12:02:30Z" {
		t.Fatalf("credential = %+v", cred)
	}
	if err := access.Revoke(context.Background(), "review", cred.Hash); err != nil || len(be.revoked) != 1 {
		t.Fatalf("revoke: %v, %v", err, be.revoked)
	}
	stream, err := access.Call(context.Background(), "review", "/peers/worker-1", "tok", []byte(`{"jsonrpc":"2.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Close()
	if be.ref != (backend.HubRef{Group: "review", Name: "enclave-a2a-hub-review"}) || be.path != "/peers/worker-1" || be.stdin != "tok\n{\"jsonrpc\":\"2.0\"}" {
		t.Fatalf("hub call %+v %s %q", be.ref, be.path, be.stdin)
	}
}
