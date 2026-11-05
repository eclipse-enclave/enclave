// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"enclave/internal/a2a"
)

const (
	// maxEventBytes bounds one SSE event; the result bound is 256 KiB by default.
	maxEventBytes = 4 << 20
	maxBodyBytes  = 4 << 20
)

// hubClient speaks the hub HTTP contract of interfaces.md section 11.
type hubClient struct {
	base   string
	token  string
	http   *http.Client
	nextID atomic.Int64
}

func newHubClient(host string, port int, token string) *hubClient {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		// The session env may carry HTTP_PROXY; the hub is reached directly.
		Proxy: nil,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", addr)
		},
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConnsPerHost:   4,
	}
	return &hubClient{
		base:  "http://" + net.JoinHostPort(host, strconv.Itoa(port)),
		token: token,
		http:  &http.Client{Transport: transport},
	}
}

func (h *hubClient) newRequest(ctx context.Context, method, path string, body []byte, accept string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.base+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set(a2a.HeaderAuthorization, a2a.BearerPrefix+h.token)
	a2a.SetProtocolHeaders(req.Header)
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// errHub is a transport-level failure: the request may or may not have reached the worker.
type errHub struct{ err error }

func (e *errHub) Error() string { return "hub unreachable: " + e.err.Error() }
func (e *errHub) Unwrap() error { return e.err }

func (h *hubClient) do(req *http.Request) (*http.Response, error) {
	resp, err := h.http.Do(req) // #nosec G704 -- the hub address comes from the host-set session env.
	if err != nil {
		return nil, &errHub{err}
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, &errHub{fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	return resp, nil
}

// listPeers fetches the peer listing; a JSON-RPC error body is returned as *a2a.Error.
func (h *hubClient) listPeers(ctx context.Context) (a2a.PeerListing, error) {
	req, err := h.newRequest(ctx, http.MethodGet, a2a.HubPeersPath, nil, "application/json")
	if err != nil {
		return a2a.PeerListing{}, err
	}
	resp, err := h.do(req)
	if err != nil {
		return a2a.PeerListing{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return a2a.PeerListing{}, &errHub{err}
	}
	var body struct {
		a2a.PeerListing
		Error *a2a.Error `json:"error"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return a2a.PeerListing{}, &errHub{fmt.Errorf("decode peer listing: %w", err)}
	}
	if body.Error != nil {
		return a2a.PeerListing{}, body.Error
	}
	return body.PeerListing, nil
}

// streamItem is one decoded stream element: a result, a JSON-RPC error, or a read failure.
type streamItem struct {
	result a2a.Result
	rpcErr *a2a.Error
	err    error
}

// call posts one JSON-RPC request to a peer and delivers results on the returned channel until it closes.
// SSE and plain JSON responses are both accepted; ctx ends the read.
func (h *hubClient) call(ctx context.Context, peer, method string, params any) (<-chan streamItem, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	id := h.nextID.Add(1)
	body, err := json.Marshal(a2a.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(strconv.FormatInt(id, 10)),
		Method:  method,
		Params:  raw,
	})
	if err != nil {
		return nil, err
	}
	accept := "application/json"
	if method == a2a.MethodSendStreamingMessage || method == a2a.MethodSubscribeToTask {
		accept = a2a.SSEContentType
	}
	req, err := h.newRequest(ctx, http.MethodPost, a2a.PeerPathPrefix+peer, body, accept)
	if err != nil {
		return nil, err
	}
	resp, err := h.do(req)
	if err != nil {
		return nil, err
	}
	items := make(chan streamItem)
	go func() {
		defer close(items)
		defer func() { _ = resp.Body.Close() }()
		send := func(it streamItem) bool {
			select {
			case items <- it:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if strings.HasPrefix(resp.Header.Get("Content-Type"), a2a.SSEContentType) {
			reader := a2a.NewSSEReader(resp.Body, maxEventBytes)
			for {
				ev, err := reader.Next()
				if errors.Is(err, io.EOF) {
					return
				}
				if err != nil {
					send(streamItem{err: &errHub{err}})
					return
				}
				it := decodeResponse(method, ev.Data)
				if !send(it) || it.rpcErr != nil || it.err != nil {
					return
				}
			}
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		if err != nil {
			send(streamItem{err: &errHub{err}})
			return
		}
		send(decodeResponse(method, data))
	}()
	return items, nil
}

func decodeResponse(method string, data []byte) streamItem {
	var resp a2a.Response
	if err := json.Unmarshal(data, &resp); err != nil {
		return streamItem{err: &errHub{fmt.Errorf("decode response: %w", err)}}
	}
	if resp.Error != nil {
		return streamItem{rpcErr: resp.Error}
	}
	result, err := decodeMethodResult(method, resp.Result)
	if err != nil {
		return streamItem{err: &errHub{err}}
	}
	return streamItem{result: result}
}

// decodeMethodResult decodes a result by method: GetTask and CancelTask answer a bare Task, the rest a StreamResponse.
func decodeMethodResult(method string, raw json.RawMessage) (a2a.Result, error) {
	switch method {
	case a2a.MethodGetTask, a2a.MethodCancelTask:
		var t a2a.Task
		if err := json.Unmarshal(raw, &t); err != nil {
			return a2a.Result{}, fmt.Errorf("decode %s result: %w", method, err)
		}
		if t.ID == "" {
			return a2a.Result{}, fmt.Errorf("decode %s result: no task id", method)
		}
		return a2a.Result{Task: &t}, nil
	}
	return a2a.DecodeResult(raw)
}

// callOnce performs a non-streaming call and returns its single result.
func (h *hubClient) callOnce(ctx context.Context, peer, method string, params any) (a2a.Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	items, err := h.call(ctx, peer, method, params)
	if err != nil {
		return a2a.Result{}, err
	}
	it, ok := <-items
	switch {
	case !ok:
		if ctx.Err() != nil {
			return a2a.Result{}, ctx.Err()
		}
		return a2a.Result{}, &errHub{errors.New("empty response")}
	case it.err != nil:
		return a2a.Result{}, it.err
	case it.rpcErr != nil:
		return a2a.Result{}, it.rpcErr
	}
	return it.result, nil
}
