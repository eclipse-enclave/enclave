// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
)

// LatestProtocolVersion is answered when the client asks for a version this server does not know.
const LatestProtocolVersion = "2025-11-25"

var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

const maxLineBytes = 32 << 20

// Server is the orchestrator's stdio MCP server (newline-delimited JSON-RPC).
type Server struct {
	client *Client
	// Liveness holds the orchestrator liveness connection while Serve runs.
	Liveness bool

	writeMu sync.Mutex
	out     io.Writer
	callsMu sync.Mutex
	calls   map[string]context.CancelFunc
	wg      sync.WaitGroup
}

var _ a2aagent.MCPClient = (*Server)(nil)

// NewServer returns a server that holds the liveness connection.
func NewServer(c *Client) *Server {
	return &Server{client: c, Liveness: true}
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Serve answers requests from in until in ends or ctx is done. In-flight
// calls then stop waiting; no task is ever canceled for it.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.out = out
	s.calls = map[string]context.CancelFunc{}
	if s.Liveness {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.client.HoldLiveness(ctx)
		}()
	}
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 64<<10), maxLineBytes)
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
		readErr <- scanner.Err()
	}()
	var err error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case err = <-readErr:
			break loop
		case line := <-lines:
			s.handle(ctx, line)
		}
	}
	cancel()
	s.wg.Wait()
	return err
}

func (s *Server) handle(ctx context.Context, line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	if line[0] == '[' {
		s.writeError(json.RawMessage("null"), a2a.CodeInvalidRequest, "batch requests are not supported")
		return
	}
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		s.writeError(json.RawMessage("null"), a2a.CodeParseError, "parse error")
		return
	}
	isRequest := len(msg.ID) > 0 && string(msg.ID) != "null"
	if msg.Method == "" {
		// A response to a request this server never sends.
		return
	}
	switch msg.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		version := LatestProtocolVersion
		if slices.Contains(protocolVersions, params.ProtocolVersion) {
			version = params.ProtocolVersion
		}
		s.writeResult(msg.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "enclave-a2a", "version": "1"},
		})
	case "ping":
		s.writeResult(msg.ID, map[string]any{})
	case "tools/list":
		s.writeResult(msg.ID, map[string]any{"tools": Tools})
	case "tools/call":
		if !isRequest {
			return
		}
		s.startCall(ctx, msg)
	case "notifications/cancelled":
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(msg.Params, &params) == nil {
			s.callsMu.Lock()
			if stop, ok := s.calls[string(params.RequestID)]; ok {
				stop()
			}
			s.callsMu.Unlock()
		}
	default:
		// Includes server/discover, which makes Claude Code fall back to initialize (P3).
		if isRequest {
			s.writeError(msg.ID, a2a.CodeMethodNotFound, "method not found")
		}
	}
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      struct {
		ProgressToken json.RawMessage `json:"progressToken"`
	} `json:"_meta"`
}

func (s *Server) startCall(ctx context.Context, msg rpcMessage) {
	var params callParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		s.writeError(msg.ID, a2a.CodeInvalidParams, "invalid tools/call params")
		return
	}
	if !slices.ContainsFunc(Tools, func(t Tool) bool { return t.Name == params.Name }) {
		s.writeError(msg.ID, a2a.CodeInvalidParams, "unknown tool: "+params.Name)
		return
	}
	callCtx, stop := context.WithCancel(ctx)
	key := string(msg.ID)
	s.callsMu.Lock()
	s.calls[key] = stop
	s.callsMu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.callsMu.Lock()
			delete(s.calls, key)
			s.callsMu.Unlock()
			stop()
		}()
		res, err := s.call(callCtx, params)
		// A canceled or abandoned call gets no response (MCP cancellation).
		if err != nil || callCtx.Err() != nil {
			return
		}
		text, mErr := json.Marshal(res)
		if mErr != nil {
			text = []byte(`{"error":"internal: encode result"}`)
		}
		s.writeResult(msg.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(text)}},
			"isError": res.IsError(),
		})
	}()
}

func (s *Server) call(ctx context.Context, params callParams) (Result, error) {
	progress := s.progressFunc(params.Meta.ProgressToken)
	switch params.Name {
	case a2aagent.MCPToolListPeers:
		var args struct{}
		if res, ok := decodeArgs(params.Arguments, &args); !ok {
			return res, nil
		}
		return s.client.ListPeers(ctx)
	case a2aagent.MCPToolSendTask:
		var args struct {
			Peer        string   `json:"peer"`
			Text        string   `json:"text"`
			Context     string   `json:"context"`
			WaitSeconds *float64 `json:"wait_seconds"`
			Retry       bool     `json:"retry"`
			MessageID   string   `json:"message_id"`
		}
		if res, ok := decodeArgs(params.Arguments, &args); !ok {
			return res, nil
		}
		wait, res, ok := waitArg(args.WaitSeconds)
		if !ok {
			return res, nil
		}
		return s.client.Send(ctx, SendRequest{
			Peer: args.Peer, Text: args.Text, Context: args.Context,
			Wait: wait, Retry: args.Retry, MessageID: args.MessageID,
		}, progress)
	case a2aagent.MCPToolGetTask:
		var args struct {
			Handle      string   `json:"handle"`
			WaitSeconds *float64 `json:"wait_seconds"`
		}
		if res, ok := decodeArgs(params.Arguments, &args); !ok {
			return res, nil
		}
		wait, res, ok := waitArg(args.WaitSeconds)
		if !ok {
			return res, nil
		}
		return s.client.Get(ctx, args.Handle, wait, progress)
	default:
		var args struct {
			Handle string `json:"handle"`
		}
		if res, ok := decodeArgs(params.Arguments, &args); !ok {
			return res, nil
		}
		return s.client.Cancel(ctx, args.Handle)
	}
}

func decodeArgs(raw json.RawMessage, v any) (Result, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errorResult("invalid arguments: %s", strings.TrimPrefix(err.Error(), "json: ")), false
	}
	return Result{}, true
}

func waitArg(seconds *float64) (*time.Duration, Result, bool) {
	if seconds == nil {
		return nil, Result{}, true
	}
	if *seconds < 0 {
		return nil, errorResult("wait_seconds must not be negative"), false
	}
	// Clamped to the send wait later; capping here only avoids overflow.
	d := time.Duration(min(*seconds, 1e6) * float64(time.Second))
	return &d, Result{}, true
}

// progressFunc emits notifications/progress for calls that carry a progress token.
func (s *Server) progressFunc(token json.RawMessage) ProgressFunc {
	if len(token) == 0 || string(token) == "null" {
		return nil
	}
	var mu sync.Mutex
	n := 0
	return func(message string) {
		mu.Lock()
		n++
		count := n
		mu.Unlock()
		s.write(map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/progress",
			"params":  map[string]any{"progressToken": token, "progress": count, "message": message},
		})
	}
}

func (s *Server) writeResult(id json.RawMessage, result any) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *Server) writeError(id json.RawMessage, code int, message string) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func (s *Server) write(msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		s.client.logf("a2a: mcp: encode: %v", err)
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.out.Write(append(data, '\n')); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		s.client.logf("a2a: mcp: write: %v", err)
	}
}
