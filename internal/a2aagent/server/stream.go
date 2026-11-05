// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"enclave/internal/a2a"
)

// streamTask sends the Task, then events until the terminal status (spec 3.1.2); a dropped stream never cancels.
func (s *Server) streamTask(ctx context.Context, w http.ResponseWriter, id json.RawMessage, c caller, taskID string, history []a2a.Message) {
	// Subscribe before reading the snapshot so no event falls between the two.
	events, unsubscribe := s.exec.Subscribe(taskID)
	defer unsubscribe()
	rec, err := s.table.Task(c.ref(), taskID)
	if err != nil {
		writeResponse(w, id, nil, s.rpcError(err))
		return
	}
	sw, err := a2a.NewSSEWriter(w)
	if err != nil {
		writeResponse(w, id, nil, &a2a.Error{Code: a2a.CodeInternalError, Message: "Internal error: streaming unavailable"})
		return
	}
	if err := writeEvent(sw, id, a2a.Result{Task: s.taskObject(rec, history)}, nil); err != nil || rec.State.Terminal() {
		return
	}
	keepalive := time.NewTicker(s.keepalive)
	defer keepalive.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				s.finishStream(sw, id, c, taskID)
				return
			}
			if err := writeEvent(sw, id, ev.Event, nil); err != nil || isTerminalEvent(ev.Event) {
				return
			}
		case <-keepalive.C:
			if err := sw.WriteComment("keepalive"); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// finishStream covers a subscription closed without a terminal event: recorded ending, else an error event.
func (s *Server) finishStream(sw *a2a.SSEWriter, id json.RawMessage, c caller, taskID string) {
	rec, err := s.table.Task(c.ref(), taskID)
	if err == nil && rec.State.Terminal() {
		_ = writeEvent(sw, id, terminalStatus(rec), nil)
		return
	}
	_ = writeEvent(sw, id, nil, &a2a.Error{Code: a2a.CodeInternalError, Message: "Internal error: task event stream ended"})
}

// writeEvent writes one SSE data event holding a full JSON-RPC response (spec 9.4.2).
func writeEvent(sw *a2a.SSEWriter, id json.RawMessage, result any, rpcErr *a2a.Error) error {
	data, err := encodeResponse(id, result, rpcErr)
	if err != nil {
		data, _ = encodeResponse(id, nil, &a2a.Error{Code: a2a.CodeInternalError, Message: "Internal error"})
	}
	return sw.WriteEvent(a2a.Event{Data: data})
}
