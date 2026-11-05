// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"enclave/internal/a2a"
	"enclave/internal/peergroup"
)

var errEventTooLarge = errors.New("sse event too large")

// readLine returns one line including its terminator, or the bytes read
// before an error; more than limit bytes is errEventTooLarge.
func readLine(br *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(line)+len(chunk) > limit {
			return nil, errEventTooLarge
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

func isBlankLine(line []byte) bool {
	return string(line) == "\n" || string(line) == "\r\n"
}

// eventData joins the data lines of one raw SSE event (WHATWG field parsing).
func eventData(event []byte) ([]byte, bool) {
	var data [][]byte
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		value, ok := bytes.CutPrefix(line, []byte("data"))
		if !ok {
			continue
		}
		if len(value) > 0 && value[0] != ':' {
			continue
		}
		value = bytes.TrimPrefix(bytes.TrimPrefix(value, []byte(":")), []byte(" "))
		data = append(data, value)
	}
	if data == nil {
		return nil, false
	}
	return bytes.Join(data, []byte("\n")), true
}

// observe reads an adapter response for the owner cache and the audit entry;
// it never changes what is relayed.
func (p *Proxy) observe(ex *exchange, method string, resp a2a.Response, pr a2a.Principal, target *member) {
	if resp.Error != nil {
		ex.fail(resp.Error)
		return
	}
	if len(resp.Result) == 0 {
		return
	}
	var (
		taskID, contextID string
		state             a2a.TaskState
		meta              map[string]any
	)
	switch method {
	case a2a.MethodGetTask, a2a.MethodCancelTask:
		var t a2a.Task
		if json.Unmarshal(resp.Result, &t) != nil {
			return
		}
		taskID, contextID, state, meta = t.ID, t.ContextID, t.Status.State, t.Metadata
		ex.texts(t.Artifacts, t.Status.Message, nil)
	default:
		r, err := a2a.DecodeResult(resp.Result)
		if err != nil {
			return
		}
		switch {
		case r.Task != nil:
			taskID, contextID, state, meta = r.Task.ID, r.Task.ContextID, r.Task.Status.State, r.Task.Metadata
			ex.texts(r.Task.Artifacts, r.Task.Status.Message, nil)
		case r.StatusUpdate != nil:
			u := r.StatusUpdate
			taskID, contextID, state, meta = u.TaskID, u.ContextID, u.Status.State, u.Metadata
			ex.texts(nil, u.Status.Message, nil)
		case r.ArtifactUpdate != nil:
			u := r.ArtifactUpdate
			taskID, contextID = u.TaskID, u.ContextID
			ex.texts([]a2a.Artifact{u.Artifact}, nil, nil)
		case r.Message != nil:
			taskID, contextID = r.Message.TaskID, r.Message.ContextID
			ex.texts(nil, nil, r.Message)
		}
	}
	if taskID != "" {
		ex.entry.TaskID = taskID
	}
	if contextID != "" {
		ex.entry.ContextID = contextID
	}
	summary := taskSummary{state: state}
	if em, ok, err := a2a.EnclaveMetaFrom(meta); ok && err == nil {
		summary.reason, summary.termination = em.Reason, em.Termination
		if em.Reason != "" {
			ex.entry.Reason = string(em.Reason)
		}
		if em.Termination != "" {
			ex.entry.Termination = string(em.Termination)
		}
	}
	p.cache.record(target.Name, target.Generation, taskID, ownerOf(pr), summary)
}

func (ex *exchange) texts(artifacts []a2a.Artifact, status *a2a.Message, msg *a2a.Message) {
	if !ex.full {
		return
	}
	for _, a := range artifacts {
		appendText(&ex.response, a.Parts)
	}
	if status != nil {
		appendText(&ex.response, status.Parts)
	}
	if msg != nil {
		appendText(&ex.response, msg.Parts)
	}
}

func appendText(b *strings.Builder, parts []a2a.Part) {
	for _, part := range parts {
		if part.Text == nil || b.Len() >= maxAuditContent {
			continue
		}
		text := *part.Text
		if room := maxAuditContent - b.Len(); len(text) > room {
			text = text[:room]
		}
		b.WriteString(text)
	}
}

// finish writes the audit entry of one JSON-RPC request.
func (p *Proxy) finish(ex *exchange) {
	ex.entry.TS = peergroup.FormatTimestamp(p.cfg.Now())
	if ex.full {
		ex.entry.Content = &AuditContent{Request: ex.request.String(), Response: ex.response.String()}
	}
	if err := p.audit.write(ex.entry); err != nil {
		p.logger.Printf("audit: %v", err)
	}
}
