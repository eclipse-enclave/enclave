// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
)

// ResultArtifactID names the artifact that carries the final message; it matches the executor's events.
const ResultArtifactID = "result"

// caller is the authenticated principal of one request, as the hub forwarded it.
type caller struct {
	principal a2a.Principal
	epoch     string
}

func (c caller) ref() a2aagent.PrincipalRef {
	return a2aagent.PrincipalRef{Member: c.principal.Member, Role: c.principal.Role, Generation: c.principal.Generation}
}

func (s *Server) serveRPC(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeResponse(w, nil, nil, a2a.NewError(a2a.CodeDeliveryUnauthenticated,
			"Unauthenticated: the outbound token is missing or wrong", a2a.DeliveryUnauthenticated, nil))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(s.maxRequest)+1))
	if err != nil {
		return
	}
	if len(body) > s.maxRequest {
		writeResponse(w, nil, nil, a2a.NewError(a2a.CodeRequestTooLarge, "Request too large", a2a.DeliveryRequestTooLarge, nil))
		return
	}
	req, rerr := a2a.DecodeRequest(body)
	if rerr != nil {
		writeResponse(w, req.ID, nil, rerr)
		return
	}
	if e := a2a.CheckVersion(r.Header); e != nil {
		writeResponse(w, req.ID, nil, e)
		return
	}
	if e := a2a.CheckExtensions(r.Header); e != nil {
		writeResponse(w, req.ID, nil, e)
		return
	}
	c, e := s.authenticate(r.Context(), r.Header)
	if e != nil {
		writeResponse(w, req.ID, nil, e)
		return
	}
	ctx := r.Context()
	switch req.Method {
	case a2a.MethodSendMessage:
		s.send(ctx, w, req, c, false)
	case a2a.MethodSendStreamingMessage:
		s.send(ctx, w, req, c, true)
	case a2a.MethodGetTask:
		s.getTask(w, req, c)
	case a2a.MethodCancelTask:
		s.cancelTask(ctx, w, req, c)
	case a2a.MethodSubscribeToTask:
		s.subscribe(ctx, w, req, c)
	case a2a.MethodListTasks:
		writeResponse(w, req.ID, nil, &a2a.Error{Code: a2a.CodeUnsupportedOperation, Message: "ListTasks is not supported"})
	case a2a.MethodGetExtendedAgentCard:
		writeResponse(w, req.ID, nil, &a2a.Error{Code: a2a.CodeExtendedAgentCardNotConfig, Message: "Extended agent card is not configured"})
	case a2a.MethodCreateTaskPushNotificationConfig, a2a.MethodGetTaskPushNotificationConfig, a2a.MethodListTaskPushNotificationConfigs, a2a.MethodDeleteTaskPushNotificationConfig:
		writeResponse(w, req.ID, nil, &a2a.Error{Code: a2a.CodePushNotificationNotSupported, Message: "Push notifications are not supported"})
	default:
		writeResponse(w, req.ID, nil, &a2a.Error{Code: a2a.CodeMethodNotFound, Message: "Method not found"})
	}
}

// authenticate reads the principal headers, which the hub sets after stripping the client's.
func (s *Server) authenticate(ctx context.Context, h http.Header) (caller, *a2a.Error) {
	c := caller{
		principal: a2a.Principal{
			Group:           h.Get(a2a.HeaderGroup),
			GroupGeneration: h.Get(a2a.HeaderGroupGeneration),
			Member:          h.Get(a2a.HeaderMember),
			Role:            a2a.Role(h.Get(a2a.HeaderRole)),
			Generation:      h.Get(a2a.HeaderGeneration),
		},
		epoch: h.Get(a2a.HeaderEpoch),
	}
	p := c.principal
	if p.Group == "" || p.GroupGeneration == "" || p.Member == "" || p.Role == "" || p.Generation == "" || c.epoch == "" {
		return c, a2a.NewError(a2a.CodeDeliveryUnauthenticated, "Unauthenticated: principal headers missing", a2a.DeliveryUnauthenticated, nil)
	}
	unknown := a2a.NewError(a2a.CodeGenerationUnknown, "Principal generation unknown", a2a.RefusalGenerationUnknown,
		map[string]string{"member": p.Member})
	if p.Group != s.cfg.Group || p.GroupGeneration != s.cfg.GroupGeneration || !generationPattern.MatchString(p.Generation) {
		return c, unknown
	}
	switch {
	case p.Role == a2a.RoleOrchestrator && p.Member != HostMemberName:
		if !s.orchestratorGenerationKnown(p.Member, p.Generation) {
			return c, unknown
		}
	case p.Role == a2a.RoleHost && p.Member == HostMemberName:
	case p.Role == a2a.RoleWorker:
		return c, a2a.NewError(a2a.CodeDeliveryRoleForbidden, "Role forbidden: workers cannot send tasks", a2a.DeliveryRoleForbidden,
			map[string]string{"member": p.Member})
	default:
		return c, a2a.NewError(a2a.CodeDeliveryUnauthenticated, "Unauthenticated: invalid principal role", a2a.DeliveryUnauthenticated, nil)
	}
	// A malformed or missing revision waits for nothing beyond the first membership.
	revision, _ := strconv.ParseUint(h.Get(a2a.HeaderRevision), 10, 64)
	current, known := s.principalCurrent(ctx, p, revision)
	switch {
	case !known:
		return c, errEpochUnconfirmed()
	case !current:
		return c, unknown
	}
	return c, nil
}

func (s *Server) send(ctx context.Context, w http.ResponseWriter, req a2a.Request, c caller, streaming bool) {
	var params a2a.SendMessageRequest
	if e := a2a.DecodeParams(req, &params); e != nil {
		writeResponse(w, req.ID, nil, invalidParams("Invalid parameters"))
		return
	}
	msg := params.Message
	if e := msg.Validate(); e != nil {
		writeResponse(w, req.ID, nil, e)
		return
	}
	if msg.Role != a2a.RoleUser {
		writeResponse(w, req.ID, nil, invalidParams("Invalid parameters: message.role must be ROLE_USER"))
		return
	}
	meta, _, err := a2a.EnclaveMetaFrom(msg.Metadata)
	if err != nil {
		writeResponse(w, req.ID, nil, invalidParams("Invalid parameters: invalid enclave metadata on the message"))
		return
	}
	reqMeta, _, err := a2a.EnclaveMetaFrom(params.Metadata)
	if err != nil {
		writeResponse(w, req.ID, nil, invalidParams("Invalid parameters: invalid enclave metadata on the request"))
		return
	}
	if meta.Principal != nil || reqMeta.Principal != nil {
		writeResponse(w, req.ID, nil, invalidParams("Invalid parameters: principal metadata is set by the adapter only"))
		return
	}
	if msg.TaskID != "" {
		// Every send is a new task in v1; nothing waits for input, so only report unknown IDs as such.
		if _, err := s.table.Task(c.ref(), msg.TaskID); err != nil {
			writeResponse(w, req.ID, nil, s.rpcError(err))
			return
		}
		writeResponse(w, req.ID, nil, invalidParams("Invalid parameters: message.taskId cannot continue a task; send to its contextId"))
		return
	}
	texts := make([]string, len(msg.Parts))
	for i, p := range msg.Parts {
		texts[i] = *p.Text
	}
	if !s.waitEpoch(ctx, c.epoch) {
		writeResponse(w, req.ID, nil, errEpochUnconfirmed())
		return
	}
	adm, err := s.table.Admit(ctx, a2aagent.AdmitRequest{
		Principal:   c.ref(),
		Epoch:       c.epoch,
		MessageID:   msg.MessageID,
		ContentHash: contentHash(msg.ContextID, texts),
		Retry:       meta.Retry,
		ContextID:   msg.ContextID,
	})
	if err != nil {
		writeResponse(w, req.ID, nil, s.rpcError(err))
		return
	}
	taskID := adm.Task.TaskID
	if !adm.Existing {
		s.exec.Submit(taskID, strings.Join(texts, "\n"))
	}

	stored := msg
	stored.TaskID = taskID
	stored.ContextID = adm.Task.ContextID
	principal := c.principal
	stored.Metadata = a2a.WithEnclaveMeta(maps.Clone(msg.Metadata), a2a.EnclaveMeta{Retry: meta.Retry, Principal: &principal})
	history := []a2a.Message{stored}

	if streaming {
		s.streamTask(ctx, w, req.ID, c, taskID, history)
		return
	}
	if params.Configuration == nil || !params.Configuration.ReturnImmediately {
		s.waitTerminal(ctx, taskID)
	}
	rec, err := s.table.Task(c.ref(), taskID)
	if err != nil {
		writeResponse(w, req.ID, nil, s.rpcError(err))
		return
	}
	writeResponse(w, req.ID, a2a.Result{Task: s.taskObject(rec, history)}, nil)
}

// waitTerminal blocks a SendMessage until the task ends; a dropped caller never cancels.
func (s *Server) waitTerminal(ctx context.Context, taskID string) {
	events, unsubscribe := s.exec.Subscribe(taskID)
	defer unsubscribe()
	for {
		select {
		case ev, ok := <-events:
			if !ok || isTerminalEvent(ev.Event) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) getTask(w http.ResponseWriter, req a2a.Request, c caller) {
	var params a2a.GetTaskRequest
	if e := a2a.DecodeParams(req, &params); e != nil || params.ID == "" {
		writeResponse(w, req.ID, nil, invalidParams("Invalid parameters: id is required"))
		return
	}
	rec, err := s.table.Task(c.ref(), params.ID)
	if err != nil {
		writeResponse(w, req.ID, nil, s.rpcError(err))
		return
	}
	writeResponse(w, req.ID, s.taskObject(rec, nil), nil)
}

func (s *Server) cancelTask(ctx context.Context, w http.ResponseWriter, req a2a.Request, c caller) {
	var params a2a.CancelTaskRequest
	if e := a2a.DecodeParams(req, &params); e != nil || params.ID == "" {
		writeResponse(w, req.ID, nil, invalidParams("Invalid parameters: id is required"))
		return
	}
	rec, err := s.table.Task(c.ref(), params.ID)
	if err != nil {
		writeResponse(w, req.ID, nil, s.rpcError(err))
		return
	}
	if rec.State.Terminal() {
		writeResponse(w, req.ID, nil, &a2a.Error{Code: a2a.CodeTaskNotCancelable, Message: "Task is not cancelable: it already ended"})
		return
	}
	final, err := s.exec.Cancel(ctx, params.ID)
	if err != nil {
		writeResponse(w, req.ID, nil, s.rpcError(err))
		return
	}
	writeResponse(w, req.ID, s.taskObject(final, nil), nil)
}

func (s *Server) subscribe(ctx context.Context, w http.ResponseWriter, req a2a.Request, c caller) {
	var params a2a.SubscribeToTaskRequest
	if e := a2a.DecodeParams(req, &params); e != nil || params.ID == "" {
		writeResponse(w, req.ID, nil, invalidParams("Invalid parameters: id is required"))
		return
	}
	rec, err := s.table.Task(c.ref(), params.ID)
	if err != nil {
		writeResponse(w, req.ID, nil, s.rpcError(err))
		return
	}
	if rec.State.Terminal() {
		writeResponse(w, req.ID, nil, &a2a.Error{Code: a2a.CodeUnsupportedOperation,
			Message: "Task already ended; use GetTask"})
		return
	}
	s.streamTask(ctx, w, req.ID, c, params.ID, nil)
}

// taskObject renders a record; termination, reason and queue position ride Task.metadata.
func (s *Server) taskObject(rec *a2aagent.TaskRecord, history []a2a.Message) *a2a.Task {
	t := &a2a.Task{
		ID:        rec.TaskID,
		ContextID: rec.ContextID,
		Status:    a2a.TaskStatus{State: rec.State, Timestamp: recordTime(rec)},
		History:   history,
	}
	meta := a2aagent.TerminalMeta(rec)
	if rec.State == a2a.TaskStateSubmitted {
		pos := s.queuePosition(rec)
		meta.QueuePosition = &pos
	}
	if meta != (a2a.EnclaveMeta{}) {
		t.Metadata = a2a.WithEnclaveMeta(nil, meta)
	}
	if a2aagent.HasResultArtifact(rec) {
		t.Artifacts = []a2a.Artifact{{ArtifactID: ResultArtifactID, Parts: []a2a.Part{a2a.TextPart(rec.Result)}}}
	}
	return t
}

// queuePosition counts the tasks ahead, the running one included, like the table does at admission.
func (s *Server) queuePosition(rec *a2aagent.TaskRecord) int {
	pos := 0
	for _, other := range s.table.Tasks() {
		if other.State == a2a.TaskStateWorking || (other.State == a2a.TaskStateSubmitted && other.Seq < rec.Seq) {
			pos++
		}
	}
	return pos
}

func recordTime(rec *a2aagent.TaskRecord) string {
	for _, ts := range []string{rec.EndedAt, rec.StartedAt, rec.AdmittedAt} {
		if ts != "" {
			return ts
		}
	}
	return ""
}

// terminalStatus rebuilds the terminal status event from a record, as the executor emits it.
func terminalStatus(rec *a2aagent.TaskRecord) a2a.Result {
	return a2a.Result{StatusUpdate: &a2a.TaskStatusUpdateEvent{
		TaskID: rec.TaskID, ContextID: rec.ContextID,
		Status:   a2a.TaskStatus{State: rec.State, Timestamp: rec.EndedAt},
		Metadata: a2a.WithEnclaveMeta(nil, a2aagent.TerminalMeta(rec)),
	}}
}

func isTerminalEvent(ev a2a.Result) bool {
	return ev.StatusUpdate != nil && ev.StatusUpdate.Status.State.Terminal()
}

// contentHash excludes metadata so that a flagged retry hashes like its original.
func contentHash(contextID string, texts []string) string {
	data, _ := json.Marshal(struct {
		ContextID string   `json:"contextId"`
		Parts     []string `json:"parts"`
	}{contextID, texts})
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// rpcError sends only enclave-range codes with data (interfaces.md section 11) and hides unexpected errors.
func (s *Server) rpcError(err error) *a2a.Error {
	var ae *a2a.Error
	if errors.As(err, &ae) {
		out := *ae
		if out.Code < -31099 || out.Code > -31000 {
			out.Data = nil
		}
		return &out
	}
	s.logf("internal error: %v", err)
	return &a2a.Error{Code: a2a.CodeInternalError, Message: "Internal error"}
}

func errEpochUnconfirmed() *a2a.Error {
	return a2a.NewError(a2a.CodeEpochUnconfirmed, "Hub epoch not confirmed by the control connection; nothing was admitted",
		a2a.RefusalEpochUnconfirmed, nil)
}

func invalidParams(msg string) *a2a.Error {
	return &a2a.Error{Code: a2a.CodeInvalidParams, Message: msg}
}

// writeResponse writes one JSON-RPC response; errors are HTTP 200 too (interfaces.md section 11).
func writeResponse(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *a2a.Error) {
	data, err := encodeResponse(id, result, rpcErr)
	if err != nil {
		data, _ = encodeResponse(id, nil, &a2a.Error{Code: a2a.CodeInternalError, Message: "Internal error"})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data) // #nosec G705 -- a JSON-RPC body served as application/json, never HTML.
}

func encodeResponse(id json.RawMessage, result any, rpcErr *a2a.Error) ([]byte, error) {
	resp := a2a.Response{JSONRPC: a2a.JSONRPCVersion, ID: id, Error: rpcErr}
	if len(resp.ID) == 0 {
		resp.ID = json.RawMessage("null")
	}
	if rpcErr == nil {
		raw, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		resp.Result = raw
	}
	return json.Marshal(resp)
}
