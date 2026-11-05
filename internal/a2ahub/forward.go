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
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/model"
)

const (
	// maxResponseBytes bounds a non-streaming adapter response.
	maxResponseBytes = 32 << 20
	// maxEventBytes bounds one SSE event relayed from an adapter.
	maxEventBytes = 16 << 20
	// maxCardBytes bounds an adapter card.
	maxCardBytes = 1 << 20
	// cardTimeout bounds a card fetch from an adapter.
	cardTimeout = 10 * time.Second
	// maxAuditContent bounds each text member of a full-mode audit entry.
	maxAuditContent = 1 << 20
)

// forwardedHeaders are the only client headers passed to an adapter; every
// identity-bearing header is dropped by construction.
var forwardedHeaders = []string{"Content-Type", "Accept", a2a.HeaderVersion, a2a.HeaderExtensions}

// relayedHeaders are the only adapter response headers passed to the client.
var relayedHeaders = []string{"Content-Type", "Cache-Control", "X-Accel-Buffering"}

// Handler routes the hub HTTP paths of interfaces.md section 11.
func (p *Proxy) Handler() http.Handler { return http.HandlerFunc(p.serveHTTP) }

func (p *Proxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == a2a.HubControlPath:
		if allowMethod(w, r, http.MethodGet) {
			p.handleControl(w, r)
		}
	case path == a2a.HubLivenessPath:
		if allowMethod(w, r, http.MethodGet) {
			p.handleLiveness(w, r)
		}
	case path == a2a.HubPeersPath:
		if allowMethod(w, r, http.MethodGet) {
			p.handleListing(w, r)
		}
	case strings.HasPrefix(path, a2a.PeerPathPrefix):
		rest := strings.TrimPrefix(path, a2a.PeerPathPrefix)
		if name, ok := strings.CutSuffix(rest, a2a.CardPath); ok && !strings.Contains(name, "/") {
			if allowMethod(w, r, http.MethodGet) {
				p.handleCard(w, r, name)
			}
			return
		}
		if rest == "" || strings.Contains(rest, "/") {
			http.NotFound(w, r)
			return
		}
		if allowMethod(w, r, http.MethodPost) {
			p.handleRPC(w, r, rest)
		}
	default:
		http.NotFound(w, r)
	}
}

func allowMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

// writeRPCError answers a JSON-RPC request: always HTTP 200 (interfaces.md section 11).
func writeRPCError(w http.ResponseWriter, id json.RawMessage, e *a2a.Error) {
	writeJSON(w, http.StatusOK, a2a.Response{JSONRPC: a2a.JSONRPCVersion, ID: id, Error: e})
}

// writeHubError answers a non-JSON-RPC path with a status and a JSON-RPC error body.
func writeHubError(w http.ResponseWriter, e *hubError) {
	writeJSON(w, e.status, a2a.Response{JSONRPC: a2a.JSONRPCVersion, Error: e.err})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data) // #nosec G705 -- JSON with a JSON content type.
}

// exchange accumulates what the audit entry of one JSON-RPC request records.
type exchange struct {
	entry    AuditEntry
	full     bool
	request  strings.Builder
	response strings.Builder
}

func (ex *exchange) fail(e *a2a.Error) {
	ex.entry.Outcome = e.Code
	if info, ok := e.Info(); ok {
		ex.entry.Reason = info.Reason
	}
}

func (p *Proxy) handleRPC(w http.ResponseWriter, r *http.Request, name string) {
	ex := &exchange{full: p.cfg.LogMode == LogFull}
	ex.entry.Target = name
	ex.entry.Epoch = p.epoch
	ex.entry.Group = p.cfg.Group
	ex.entry.Revision = uint64(p.Revision())
	defer p.finish(ex)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, p.cfg.MaxRequestBytes+requestEnvelopeBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			herr := errTooLarge()
			ex.fail(herr.err)
			writeRPCError(w, nil, herr.err)
		}
		return
	}
	req, rerr := a2a.DecodeRequest(body)
	ex.entry.Method = req.Method
	if rerr != nil {
		ex.fail(rerr)
		writeRPCError(w, req.ID, rerr)
		return
	}
	refuse := func(e *a2a.Error) {
		ex.fail(e)
		writeRPCError(w, req.ID, e)
	}
	c, herr := p.authenticate(r, a2a.RoleOrchestrator, a2a.RoleHost)
	if herr != nil {
		refuse(herr.err)
		return
	}
	defer p.unregister(c)
	pr := c.cred.principal
	ex.entry.Principal, ex.entry.Role, ex.entry.Generation = pr.Member, string(pr.Role), pr.Generation
	ex.entry.Revision = uint64(c.st.revision)
	if e := a2a.CheckVersion(r.Header); e != nil {
		refuse(e)
		return
	}
	if e := a2a.CheckExtensions(r.Header); e != nil {
		refuse(e)
		return
	}
	if model.SanitizeSessionName(name) != name {
		refuse(errUnknownMember("").err)
		return
	}
	target, herr := p.setTarget(c, name)
	if herr != nil {
		refuse(herr.err)
		return
	}
	taskID := requestFacts(req, ex)
	if taskID != "" {
		if o, ok := p.cache.owner(target.Name, target.Generation, taskID); ok && o != ownerOf(pr) {
			refuse(&a2a.Error{Code: a2a.CodeTaskNotFound, Message: "Task not found"})
			return
		}
	}

	up, err := http.NewRequestWithContext(c.ctx, http.MethodPost, adapterURL(target, a2a.AdapterRPCPath), bytes.NewReader(body))
	if err != nil {
		refuse(errUnreachable(target.Name).err)
		return
	}
	for _, h := range forwardedHeaders {
		if v := r.Header.Values(h); len(v) > 0 {
			up.Header[h] = v
		}
	}
	p.setUpstreamIdentity(up.Header, c, target)
	resp, err := p.client.Do(up) // #nosec G704 -- target address comes from the verified snapshot, never from the request.
	if err != nil {
		if rev := revocationError(c.ctx, c); rev != nil {
			refuse(rev.err)
			return
		}
		refuse(errUnreachable(target.Name).err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxCardBytes))
		e := errUnreachable(target.Name).err
		e.Data = withHTTPStatus(target.Name, resp.StatusCode)
		refuse(e)
		return
	}
	ctype := resp.Header.Get("Content-Type")
	if strings.HasPrefix(strings.TrimSpace(strings.ToLower(ctype)), a2a.SSEContentType) {
		p.relayStream(w, resp, req, c, target, ex)
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if rev := revocationError(c.ctx, c); rev != nil {
			refuse(rev.err)
			return
		}
		refuse(errUnreachable(target.Name).err)
		return
	}
	if len(data) > maxResponseBytes {
		refuse(&a2a.Error{Code: a2a.CodeInvalidAgentResponse, Message: "Invalid agent response"})
		return
	}
	copyHeaders(w.Header(), resp.Header, relayedHeaders)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	var rpcResp a2a.Response
	if json.Unmarshal(data, &rpcResp) == nil {
		p.observe(ex, req.Method, rpcResp, pr, target)
	}
}

func withHTTPStatus(member string, status int) json.RawMessage {
	e := a2a.NewError(0, "", a2a.DeliveryAdapterUnreachable, map[string]string{"member": member, "httpStatus": strconv.Itoa(status)})
	return e.Data
}

func adapterURL(m *member, path string) string {
	return "http://" + net.JoinHostPort(m.gateway.String(), strconv.Itoa(m.AdapterPort)) + path
}

// setUpstreamIdentity sets the outbound token and the authenticated principal (interfaces.md section 11).
func (p *Proxy) setUpstreamIdentity(h http.Header, c *caller, target *member) {
	pr := c.cred.principal
	h.Set(a2a.HeaderAuthorization, a2a.BearerPrefix+target.outbound)
	h.Set(a2a.HeaderGroup, pr.Group)
	h.Set(a2a.HeaderGroupGeneration, pr.GroupGeneration)
	h.Set(a2a.HeaderMember, pr.Member)
	h.Set(a2a.HeaderRole, string(pr.Role))
	h.Set(a2a.HeaderGeneration, pr.Generation)
	h.Set(a2a.HeaderEpoch, p.epoch)
	h.Set(a2a.HeaderRevision, strconv.FormatUint(uint64(c.st.revision), 10))
}

func copyHeaders(dst, src http.Header, names []string) {
	for _, h := range names {
		if v := src.Values(h); len(v) > 0 {
			dst[h] = v
		}
	}
}

// requestFacts records the task and context a request names and returns the
// task ID that the owner cache is consulted for.
func requestFacts(req a2a.Request, ex *exchange) string {
	switch req.Method {
	case a2a.MethodSendMessage, a2a.MethodSendStreamingMessage:
		var params a2a.SendMessageRequest
		if json.Unmarshal(req.Params, &params) == nil {
			ex.entry.TaskID = params.Message.TaskID
			ex.entry.ContextID = params.Message.ContextID
			if ex.full {
				appendText(&ex.request, params.Message.Parts)
			}
		}
		return ""
	case a2a.MethodGetTask, a2a.MethodCancelTask, a2a.MethodSubscribeToTask:
		var params struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(req.Params, &params) == nil {
			ex.entry.TaskID = params.ID
			return params.ID
		}
	}
	return ""
}

// relayStream passes adapter SSE through event by event, flushing each one
// unchanged; an error after the stream started is one final error event.
func (p *Proxy) relayStream(w http.ResponseWriter, resp *http.Response, req a2a.Request, c *caller, target *member, ex *exchange) {
	rc := http.NewResponseController(w)
	copyHeaders(w.Header(), resp.Header, relayedHeaders)
	w.Header().Set("Content-Type", a2a.SSEContentType)
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()
	final := func(e *a2a.Error) {
		ex.fail(e)
		data, err := json.Marshal(a2a.Response{JSONRPC: a2a.JSONRPCVersion, ID: req.ID, Error: e})
		if err != nil {
			return
		}
		_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
		_ = rc.Flush()
	}
	br := bufio.NewReaderSize(resp.Body, 32<<10)
	var event []byte
	for {
		line, err := readLine(br, maxEventBytes-len(event))
		event = append(event, line...)
		if isBlankLine(line) {
			if _, werr := w.Write(event); werr != nil { // #nosec G705 -- SSE relayed unchanged by contract, not HTML.
				return
			}
			if rc.Flush() != nil {
				return
			}
			if data, ok := eventData(event); ok {
				var rpcResp a2a.Response
				if json.Unmarshal(data, &rpcResp) == nil {
					p.observe(ex, req.Method, rpcResp, c.cred.principal, target)
				}
			}
			event = event[:0]
		}
		if err == nil {
			continue
		}
		switch {
		case errors.Is(err, errEventTooLarge):
			final(&a2a.Error{Code: a2a.CodeInvalidAgentResponse, Message: "Invalid agent response"})
		case errors.Is(err, io.EOF):
			// Clean end; a trailing partial event is never dispatched by SSE readers.
		default:
			if rev := revocationError(c.ctx, c); rev != nil {
				final(rev.err)
			} else if c.ctx.Err() == nil {
				final(errUnreachable(target.Name).err)
			}
			// Otherwise the client went away; dropping a stream never cancels the task.
		}
		return
	}
}
