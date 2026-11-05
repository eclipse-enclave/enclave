// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux

package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/a2aagent/executor/fakedriver"
)

func TestCard(t *testing.T) {
	e := newEnv(t, envSetup{})
	req, _ := http.NewRequest(http.MethodGet, e.url+a2a.CardPath, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("card without token: HTTP %d, want 401", resp.StatusCode)
	}

	req.Header.Set(a2a.HeaderAuthorization, a2a.BearerPrefix+"outbound-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "max-age=0" {
		t.Fatalf("card: HTTP %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	host, port := e.hub.hostPort()
	wantURL := "http://" + host + ":" + itoa(port) + "/peers/worker-1"
	if card.Name != "worker-1" || card.Description != "reviews diffs" || len(card.SupportedInterfaces) != 1 ||
		card.SupportedInterfaces[0].URL != wantURL || card.SupportedInterfaces[0].ProtocolBinding != "JSONRPC" ||
		card.SupportedInterfaces[0].ProtocolVersion != "1.0" {
		t.Fatalf("card identity: %+v", card)
	}
	if card.Capabilities.Streaming == nil || !*card.Capabilities.Streaming || card.Capabilities.PushNotifications != nil ||
		card.Capabilities.ExtendedAgentCard != nil {
		t.Fatalf("capabilities: %+v", card.Capabilities)
	}
	if len(card.Capabilities.Extensions) != 1 {
		t.Fatalf("extensions: %+v", card.Capabilities.Extensions)
	}
	ext := card.Capabilities.Extensions[0]
	if ext.URI != a2a.ExtensionURI || !ext.Required || ext.Params["queueDepth"] != float64(4) {
		t.Fatalf("extension: %+v", ext)
	}
	if card.SecuritySchemes["bearer"].HTTPAuth == nil || card.SecuritySchemes["bearer"].HTTPAuth.Scheme != "Bearer" ||
		len(card.SecurityRequirements) != 1 {
		t.Fatalf("security: %+v %+v", card.SecuritySchemes, card.SecurityRequirements)
	}
	if len(card.Skills) == 0 || card.DefaultInputModes[0] != "text/plain" {
		t.Fatalf("skills or modes: %+v", card)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestRefusalsBeforeDispatch(t *testing.T) {
	e := newEnv(t, envSetup{tweakCfg: func(c *a2aagent.Config) { c.MaxRequestBytes = 4096 }})
	worker := orch
	worker.Member, worker.Role = "worker-2", a2a.RoleWorker
	foreignGroup := orch
	foreignGroup.GroupGeneration = "00000000000000ee"
	badGen := orch
	badGen.Generation = "xyz"
	hostAsOrch := host
	hostAsOrch.Role = a2a.RoleOrchestrator
	send := sendParams(message("m1", "hi"))
	cases := []struct {
		name   string
		method string
		params any
		req    request
		code   int
		reason string
	}{
		{"no token", a2a.MethodGetTask, map[string]string{"id": "x"}, request{drop: []string{a2a.HeaderAuthorization}}, a2a.CodeDeliveryUnauthenticated, a2a.DeliveryUnauthenticated},
		{"wrong token", a2a.MethodGetTask, map[string]string{"id": "x"}, request{headers: map[string]string{a2a.HeaderAuthorization: "Bearer inbound-token"}}, a2a.CodeDeliveryUnauthenticated, a2a.DeliveryUnauthenticated},
		{"no version", a2a.MethodGetTask, map[string]string{"id": "x"}, request{drop: []string{a2a.HeaderVersion}}, a2a.CodeVersionNotSupported, ""},
		{"old version", a2a.MethodGetTask, map[string]string{"id": "x"}, request{headers: map[string]string{a2a.HeaderVersion: "0.3"}}, a2a.CodeVersionNotSupported, ""},
		{"no extension", a2a.MethodGetTask, map[string]string{"id": "x"}, request{drop: []string{a2a.HeaderExtensions}}, a2a.CodeExtensionSupportRequired, ""},
		{"no principal", a2a.MethodGetTask, map[string]string{"id": "x"}, request{drop: []string{a2a.HeaderMember}}, a2a.CodeDeliveryUnauthenticated, a2a.DeliveryUnauthenticated},
		{"no epoch", a2a.MethodGetTask, map[string]string{"id": "x"}, request{drop: []string{a2a.HeaderEpoch}}, a2a.CodeDeliveryUnauthenticated, a2a.DeliveryUnauthenticated},
		{"foreign group generation", a2a.MethodGetTask, map[string]string{"id": "x"}, request{principal: &foreignGroup}, a2a.CodeGenerationUnknown, a2a.RefusalGenerationUnknown},
		{"malformed generation", a2a.MethodGetTask, map[string]string{"id": "x"}, request{principal: &badGen}, a2a.CodeGenerationUnknown, a2a.RefusalGenerationUnknown},
		{"worker principal", a2a.MethodSendMessage, send, request{principal: &worker}, a2a.CodeDeliveryRoleForbidden, a2a.DeliveryRoleForbidden},
		{"host member as orchestrator", a2a.MethodGetTask, map[string]string{"id": "x"}, request{principal: &hostAsOrch}, a2a.CodeDeliveryUnauthenticated, a2a.DeliveryUnauthenticated},
		{"too large", a2a.MethodSendMessage, sendParams(message("m1", strings.Repeat("x", 5000))), request{}, a2a.CodeRequestTooLarge, a2a.DeliveryRequestTooLarge},
		{"parse error", "", nil, request{body: []byte("{")}, a2a.CodeParseError, ""},
		{"invalid request", "", nil, request{body: []byte(`{"jsonrpc":"2.0","method":"GetTask"}`)}, a2a.CodeInvalidRequest, ""},
		{"unknown method", "Frobnicate", map[string]string{}, request{}, a2a.CodeMethodNotFound, ""},
		{"list tasks", a2a.MethodListTasks, map[string]string{}, request{}, a2a.CodeUnsupportedOperation, ""},
		{"extended card", a2a.MethodGetExtendedAgentCard, map[string]string{}, request{}, a2a.CodeExtendedAgentCardNotConfig, ""},
		{"push create", a2a.MethodCreateTaskPushNotificationConfig, map[string]string{}, request{}, a2a.CodePushNotificationNotSupported, ""},
		{"push list", a2a.MethodListTaskPushNotificationConfigs, map[string]string{}, request{}, a2a.CodePushNotificationNotSupported, ""},
		{"push get", a2a.MethodGetTaskPushNotificationConfig, map[string]string{}, request{}, a2a.CodePushNotificationNotSupported, ""},
		{"push delete", a2a.MethodDeleteTaskPushNotificationConfig, map[string]string{}, request{}, a2a.CodePushNotificationNotSupported, ""},
		{"get unknown task", a2a.MethodGetTask, map[string]string{"id": "nope"}, request{}, a2a.CodeTaskNotFound, ""},
		{"get without id", a2a.MethodGetTask, map[string]string{}, request{}, a2a.CodeInvalidParams, ""},
		{"cancel unknown task", a2a.MethodCancelTask, map[string]string{"id": "nope"}, request{}, a2a.CodeTaskNotFound, ""},
		{"subscribe unknown task", a2a.MethodSubscribeToTask, map[string]string{"id": "nope"}, request{}, a2a.CodeTaskNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, e.call(tc.method, tc.params, tc.req), tc.code, tc.reason)
		})
	}

	t.Run("parse error has null id", func(t *testing.T) {
		resp := e.call("", nil, request{body: []byte("{")})
		if string(resp.ID) != "null" {
			t.Fatalf("id = %s, want null", resp.ID)
		}
	})
}

func TestSendValidation(t *testing.T) {
	e := newEnv(t, envSetup{})
	e.waitConnected()
	withMeta := func(m a2a.Message, md map[string]any) a2a.Message { m.Metadata = md; return m }
	agentRole := message("m1", "hi")
	agentRole.Role = a2a.RoleAgent
	dataPart := a2a.Message{MessageID: "m1", Role: a2a.RoleUser, Parts: []a2a.Part{{Data: json.RawMessage(`{"a":1}`)}}}
	withTask := message("m1", "hi")
	withTask.TaskID = "unknown"
	cases := []struct {
		name   string
		params any
		code   int
	}{
		{"ill-typed params", map[string]any{"message": "text"}, a2a.CodeInvalidParams},
		{"no message id", sendParams(message("", "hi")), a2a.CodeInvalidParams},
		{"no parts", sendParams(a2a.Message{MessageID: "m1", Role: a2a.RoleUser}), a2a.CodeInvalidParams},
		{"agent role", sendParams(agentRole), a2a.CodeInvalidParams},
		{"data part", sendParams(dataPart), a2a.CodeContentTypeNotSupported},
		{"client principal on message", sendParams(withMeta(message("m1", "hi"), map[string]any{
			a2a.ExtensionURI: map[string]any{"principal": map[string]any{"member": "orch"}}})), a2a.CodeInvalidParams},
		{"client principal on request", a2a.SendMessageRequest{Message: message("m1", "hi"), Metadata: map[string]any{
			a2a.ExtensionURI: map[string]any{"principal": map[string]any{"member": "orch"}}}}, a2a.CodeInvalidParams},
		{"bad enclave metadata", sendParams(withMeta(message("m1", "hi"), map[string]any{a2a.ExtensionURI: "x"})), a2a.CodeInvalidParams},
		{"unknown task id", sendParams(withTask), a2a.CodeTaskNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := e.call(a2a.MethodSendMessage, tc.params, request{})
			wantError(t, resp, tc.code, "")
			if tc.code == a2a.CodeInvalidParams && strings.Contains(resp.Error.Message, "json:") {
				t.Fatalf("-32602 leaks the decoder error: %q", resp.Error.Message)
			}
		})
	}
	if tasks := e.tbl.Tasks(); len(tasks) != 0 {
		t.Fatalf("refused sends admitted %d task(s)", len(tasks))
	}
}

func TestStreamingSendCompletes(t *testing.T) {
	e := newEnv(t, envSetup{mode: fakedriver.ModeComplete})
	e.waitConnected()

	events := e.stream(a2a.MethodSendStreamingMessage, sendParams(message("m1", "hello")), request{})
	if len(events) < 3 {
		t.Fatalf("got %d events, want at least task, artifact and status", len(events))
	}
	first := result(t, events[0])
	if first.Task == nil {
		t.Fatalf("first event is not the task: %+v", first)
	}
	tk := first.Task
	if tk.Status.State != a2a.TaskStateSubmitted && tk.Status.State != a2a.TaskStateWorking {
		t.Fatalf("first task state %s", tk.Status.State)
	}
	if tk.Status.State == a2a.TaskStateSubmitted {
		if m := meta(t, tk.Metadata); m.QueuePosition == nil || *m.QueuePosition != 0 {
			t.Fatalf("queue position: %+v", m)
		}
	}
	if len(tk.History) != 1 {
		t.Fatalf("history: %+v", tk.History)
	}
	stored := meta(t, tk.History[0].Metadata)
	if stored.Principal == nil || *stored.Principal != orch {
		t.Fatalf("stored principal = %+v, want %+v", stored.Principal, orch)
	}
	var artifact string
	for _, ev := range events[1 : len(events)-1] {
		if r := result(t, ev); r.ArtifactUpdate != nil {
			artifact = *r.ArtifactUpdate.Artifact.Parts[0].Text
		}
	}
	if !strings.Contains(artifact, "from=orch role=orchestrator generation=0000000000000001 task="+tk.ID) ||
		!strings.HasSuffix(artifact, "]\n\nhello") {
		t.Fatalf("artifact text = %q", artifact)
	}
	last := result(t, events[len(events)-1])
	if last.StatusUpdate == nil || last.StatusUpdate.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("last event: %+v", last)
	}
	if m := meta(t, last.StatusUpdate.Metadata); m.Termination != a2a.TerminationConfirmed {
		t.Fatalf("terminal metadata: %+v", m)
	}

	got := task(t, e.call(a2a.MethodGetTask, map[string]string{"id": tk.ID}, request{}))
	if got.Status.State != a2a.TaskStateCompleted || len(got.Artifacts) != 1 || *got.Artifacts[0].Parts[0].Text != artifact {
		t.Fatalf("GetTask: %+v", got)
	}
	if m := meta(t, got.Metadata); m.Termination != a2a.TerminationConfirmed || m.QueuePosition != nil {
		t.Fatalf("GetTask metadata: %+v", m)
	}

	t.Run("subscribe to terminal task", func(t *testing.T) {
		wantError(t, e.call(a2a.MethodSubscribeToTask, map[string]string{"id": tk.ID}, request{}), a2a.CodeUnsupportedOperation, "")
	})
	t.Run("cancel terminal task", func(t *testing.T) {
		wantError(t, e.call(a2a.MethodCancelTask, map[string]string{"id": tk.ID}, request{}), a2a.CodeTaskNotCancelable, "")
	})
	t.Run("other principal sees no task", func(t *testing.T) {
		wantError(t, e.call(a2a.MethodGetTask, map[string]string{"id": tk.ID}, request{principal: &host}), a2a.CodeTaskNotFound, "")
	})
	t.Run("flagged retry returns the original", func(t *testing.T) {
		retry := message("m1", "hello")
		retry.Metadata = map[string]any{a2a.ExtensionURI: map[string]any{"retry": true}}
		r := result(t, e.call(a2a.MethodSendMessage, sendParams(retry), request{}))
		if r.Task == nil || r.Task.ID != tk.ID || r.Task.Status.State != a2a.TaskStateCompleted {
			t.Fatalf("retry: %+v", r.Task)
		}
	})
	t.Run("unflagged reuse is refused", func(t *testing.T) {
		wantError(t, e.call(a2a.MethodSendMessage, sendParams(message("m1", "hello")), request{}),
			a2a.CodeMessageIDConflict, "MESSAGE_ID_REUSED")
	})
	t.Run("reuse with other content is refused", func(t *testing.T) {
		retry := message("m1", "other")
		retry.Metadata = map[string]any{a2a.ExtensionURI: map[string]any{"retry": true}}
		wantError(t, e.call(a2a.MethodSendMessage, sendParams(retry), request{}), a2a.CodeMessageIDConflict, "MESSAGE_ID_CONFLICT")
	})
	t.Run("blocking send continues the context", func(t *testing.T) {
		next := message("m2", "again")
		next.ContextID = tk.ContextID
		r := result(t, e.call(a2a.MethodSendMessage, sendParams(next), request{}))
		if r.Task == nil || r.Task.ContextID != tk.ContextID || r.Task.Status.State != a2a.TaskStateCompleted {
			t.Fatalf("blocking send: %+v", r.Task)
		}
	})
	t.Run("context of another principal is unknown", func(t *testing.T) {
		next := message("m3", "again")
		next.ContextID = tk.ContextID
		wantError(t, e.call(a2a.MethodSendMessage, sendParams(next), request{principal: &host}), a2a.CodeContextUnknown, "CONTEXT_UNKNOWN")
	})
	t.Run("return immediately", func(t *testing.T) {
		params := a2a.SendMessageRequest{Message: message("m4", "later"), Configuration: &a2a.SendMessageConfiguration{ReturnImmediately: true}}
		r := result(t, e.call(a2a.MethodSendMessage, params, request{principal: &host}))
		if r.Task == nil || r.Task.Status.State.Terminal() {
			t.Fatalf("returnImmediately: %+v", r.Task)
		}
		waitFor(t, "host task", func() bool {
			got := task(t, e.call(a2a.MethodGetTask, map[string]string{"id": r.Task.ID}, request{principal: &host}))
			return got.Status.State == a2a.TaskStateCompleted
		})
	})
}

func TestCancelRunningTask(t *testing.T) {
	e := newEnv(t, envSetup{mode: fakedriver.ModeHang})
	e.waitConnected()
	events := e.streamChan(a2a.MethodSendStreamingMessage, sendParams(message("m1", "work")), request{})
	first := result(t, <-events)
	if first.Task == nil {
		t.Fatalf("first event: %+v", first)
	}
	id := first.Task.ID
	waitFor(t, "working", func() bool {
		got := task(t, e.call(a2a.MethodGetTask, map[string]string{"id": id}, request{}))
		return got.Status.State == a2a.TaskStateWorking
	})

	// Only the first event of the queued task's stream is read; the stream stays open.
	second := e.streamChan(a2a.MethodSendStreamingMessage, sendParams(message("m2", "queued")), request{})
	if queued := result(t, <-second).Task; queued == nil || queued.Status.State != a2a.TaskStateSubmitted {
		t.Fatalf("queued send: %+v", queued)
	}
	subscribed := e.streamChan(a2a.MethodSubscribeToTask, map[string]string{"id": id}, request{})
	if r := result(t, <-subscribed); r.Task == nil || r.Task.ID != id {
		t.Fatalf("subscribe first event: %+v", r)
	}

	canceled := task(t, e.call(a2a.MethodCancelTask, map[string]string{"id": id}, request{}))
	if canceled.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("cancel: %+v", canceled)
	}
	if m := meta(t, canceled.Metadata); m.Termination != a2a.TerminationConfirmed || m.Reason != a2a.ReasonCancel {
		t.Fatalf("cancel metadata: %+v", m)
	}
	for _, ch := range []<-chan a2a.Response{events, subscribed} {
		var last a2a.Result
		for resp := range ch {
			last = result(t, resp)
		}
		if last.StatusUpdate == nil || last.StatusUpdate.Status.State != a2a.TaskStateCanceled {
			t.Fatalf("stream did not end with the canceled status: %+v", last)
		}
	}
	wantError(t, e.call(a2a.MethodCancelTask, map[string]string{"id": id}, request{}), a2a.CodeTaskNotCancelable, "")
}

func TestQueuePositionWhileSubmitted(t *testing.T) {
	e := newEnv(t, envSetup{mode: fakedriver.ModeHang})
	e.waitConnected()
	params := func(id string) a2a.SendMessageRequest {
		return a2a.SendMessageRequest{Message: message(id, id), Configuration: &a2a.SendMessageConfiguration{ReturnImmediately: true}}
	}
	running := result(t, e.call(a2a.MethodSendMessage, params("m1"), request{})).Task
	waitFor(t, "working", func() bool {
		return task(t, e.call(a2a.MethodGetTask, map[string]string{"id": running.ID}, request{})).Status.State == a2a.TaskStateWorking
	})
	queued := result(t, e.call(a2a.MethodSendMessage, params("m2"), request{})).Task
	got := task(t, e.call(a2a.MethodGetTask, map[string]string{"id": queued.ID}, request{}))
	if m := meta(t, got.Metadata); got.Status.State != a2a.TaskStateSubmitted || m.QueuePosition == nil || *m.QueuePosition != 1 {
		t.Fatalf("queued task: state %s, metadata %+v", got.Status.State, m)
	}
	canceled := task(t, e.call(a2a.MethodCancelTask, map[string]string{"id": queued.ID}, request{}))
	if m := meta(t, canceled.Metadata); canceled.Status.State != a2a.TaskStateCanceled || m.Termination != a2a.TerminationNotApplicable {
		t.Fatalf("canceled queued task: %+v", canceled)
	}
}

func TestEmptyReplyCompletesWithEmptyArtifact(t *testing.T) {
	e := newEnv(t, envSetup{mode: fakedriver.ModeEmpty})
	e.waitConnected()
	r := result(t, e.call(a2a.MethodSendMessage, sendParams(message("m1", "hello")), request{}))
	if r.Task == nil || r.Task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("send: %+v", r.Task)
	}
	got := task(t, e.call(a2a.MethodGetTask, map[string]string{"id": r.Task.ID}, request{}))
	if len(got.Artifacts) != 1 || got.Artifacts[0].Parts[0].Text == nil || *got.Artifacts[0].Parts[0].Text != "" {
		t.Fatalf("artifacts: %+v", got.Artifacts)
	}
}

func TestTruncatedResultOnTheWire(t *testing.T) {
	s := &Server{}
	rec := &a2aagent.TaskRecord{
		TaskID: "t1", ContextID: "c1", State: a2a.TaskStateCompleted, Termination: a2a.TerminationConfirmed,
		Result: "partial" + a2a.TruncatedMarker, Truncated: true,
	}
	tk := s.taskObject(rec, nil)
	if m := meta(t, tk.Metadata); !m.Truncated || m.Termination != a2a.TerminationConfirmed {
		t.Fatalf("task metadata: %+v", m)
	}
	if len(tk.Artifacts) != 1 || !strings.HasSuffix(*tk.Artifacts[0].Parts[0].Text, a2a.TruncatedMarker) {
		t.Fatalf("artifacts: %+v", tk.Artifacts)
	}
	if m := meta(t, terminalStatus(rec).StatusUpdate.Metadata); !m.Truncated {
		t.Fatalf("terminal status metadata: %+v", m)
	}
}
