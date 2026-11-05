// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
)

type mcpPeer struct {
	t     *testing.T
	in    *io.PipeWriter
	lines chan map[string]any
	done  chan error
}

func startServer(t *testing.T, c *Client, liveness bool) *mcpPeer {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := NewServer(c)
	srv.Liveness = liveness
	p := &mcpPeer{t: t, in: inW, lines: make(chan map[string]any, 64), done: make(chan error, 1)}
	go func() {
		p.done <- srv.Serve(context.Background(), inR, outW)
		_ = outW.Close()
	}()
	go func() {
		scanner := bufio.NewScanner(outR)
		scanner.Buffer(make([]byte, 1<<20), 1<<24)
		for scanner.Scan() {
			var msg map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				t.Errorf("server wrote invalid JSON: %s", scanner.Bytes())
				continue
			}
			p.lines <- msg
		}
		close(p.lines)
	}()
	t.Cleanup(func() { _ = inW.Close() })
	return p
}

func (p *mcpPeer) send(msg string) {
	p.t.Helper()
	if _, err := io.WriteString(p.in, msg+"\n"); err != nil {
		p.t.Fatal(err)
	}
}

func (p *mcpPeer) next() map[string]any {
	p.t.Helper()
	select {
	case msg, ok := <-p.lines:
		if !ok {
			p.t.Fatal("server output closed")
		}
		return msg
	case <-time.After(5 * time.Second):
		p.t.Fatal("no message from server")
	}
	return nil
}

// toolResult decodes a tools/call response into the result JSON and the isError flag.
func toolResult(t *testing.T, msg map[string]any) (Result, bool) {
	t.Helper()
	result, ok := msg["result"].(map[string]any)
	if !ok {
		t.Fatalf("not a result: %v", msg)
	}
	content := result["content"].([]any)[0].(map[string]any)
	var res Result
	if err := json.Unmarshal([]byte(content["text"].(string)), &res); err != nil {
		t.Fatal(err)
	}
	return res, result["isError"].(bool)
}

func TestMCPHandshake(t *testing.T) {
	h := newFakeHub(t)
	p := startServer(t, newTestClient(t, h, ""), false)

	// Claude Code probes server/discover first and falls back on -32601 (P3).
	p.send(`{"jsonrpc":"2.0","id":"server-discover-probe-1","method":"server/discover","params":{"_meta":{"protocolVersion":"2026-07-28"}}}`)
	msg := p.next()
	if msg["id"] != "server-discover-probe-1" || msg["error"].(map[string]any)["code"].(float64) != a2a.CodeMethodNotFound {
		t.Fatalf("discover = %v", msg)
	}
	p.send(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"roots":{},"elicitation":{}},"clientInfo":{"name":"claude-code"}}}`)
	msg = p.next()
	result := msg["result"].(map[string]any)
	if result["protocolVersion"] != "2025-11-25" || result["capabilities"].(map[string]any)["tools"] == nil {
		t.Fatalf("initialize = %v", msg)
	}
	p.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	p.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2099-01-01"}}`)
	if got := p.next()["result"].(map[string]any)["protocolVersion"]; got != LatestProtocolVersion {
		t.Fatalf("unknown version answered with %v", got)
	}
	p.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := p.next()["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, tool := range tools {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "list_peers,send_task,get_task,cancel_task" {
		t.Fatalf("tools = %v", names)
	}
	p.send(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	if p.next()["id"].(float64) != 3 {
		t.Fatal("ping")
	}
	p.send(`not json`)
	if p.next()["error"].(map[string]any)["code"].(float64) != a2a.CodeParseError {
		t.Fatal("parse error")
	}
	_ = p.in.Close()
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return at EOF")
	}
}

func TestMCPSendWithProgress(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	c.progressInterval = 40 * time.Millisecond
	p := startServer(t, c, false)
	go func() {
		id := h.waitTasks(1)[0]
		h.start(id)
		time.Sleep(150 * time.Millisecond)
		h.finish(id, "all green")
	}()
	p.send(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"send_task","arguments":{"peer":"builder","text":"run tests"},"_meta":{"progressToken":42,"claudecode/toolUseId":"toolu_1"}}}`)
	progress := 0
	for {
		msg := p.next()
		if msg["method"] == "notifications/progress" {
			params := msg["params"].(map[string]any)
			if params["progressToken"].(float64) != 42 || params["progress"].(float64) != float64(progress+1) {
				t.Fatalf("progress = %v", params)
			}
			progress++
			continue
		}
		res, isErr := toolResult(t, msg)
		if isErr || res.State != string(a2a.TaskStateCompleted) || res.Message != "all green" || !strings.HasPrefix(res.Handle, "task:") {
			t.Fatalf("result = %+v", res)
		}
		break
	}
	if progress == 0 {
		t.Fatal("no progress notifications")
	}
}

func TestMCPCancelledCallStopsWaitingOnly(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	p := startServer(t, c, false)
	p.send(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"send_task","arguments":{"peer":"builder","text":"long"}}}`)
	h.waitTasks(1)
	time.Sleep(20 * time.Millisecond)
	p.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":9,"reason":"SdkError: Request timed out"}}`)
	// The next response must be the ping: the cancelled call gets none.
	p.send(`{"jsonrpc":"2.0","id":10,"method":"ping"}`)
	if msg := p.next(); msg["id"].(float64) != 10 {
		t.Fatalf("got %v", msg)
	}
	if h.cancelCount() != 0 {
		t.Fatal("MCP cancellation canceled the task")
	}
	p.send(`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"get_task","arguments":{}}}`)
	res, _ := toolResult(t, p.next())
	if len(res.Recent) != 1 || res.Recent[0].Handle == "" {
		t.Fatalf("recent = %+v", res)
	}
	p.send(`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"cancel_task","arguments":{"handle":"` + res.Recent[0].Handle + `"}}}`)
	res, isErr := toolResult(t, p.next())
	if isErr || res.State != string(a2a.TaskStateCanceled) || h.cancelCount() != 1 {
		t.Fatalf("cancel = %+v", res)
	}
}

func TestMCPArgumentErrors(t *testing.T) {
	h := newFakeHub(t)
	p := startServer(t, newTestClient(t, h, ""), false)
	p.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_task","arguments":{"peer":"builder","text":"x","task_id":"t1"}}}`)
	res, isErr := toolResult(t, p.next())
	if !isErr || !strings.Contains(res.Error, `unknown field "task_id"`) {
		t.Fatalf("unknown field = %+v", res)
	}
	p.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_task","arguments":{"handle":"task:builder:` + testWorkerG + `:t1","wait_seconds":-1}}}`)
	if res, isErr = toolResult(t, p.next()); !isErr || !strings.Contains(res.Error, "negative") {
		t.Fatalf("negative wait = %+v", res)
	}
	p.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"delete_everything","arguments":{}}}`)
	if msg := p.next(); msg["error"].(map[string]any)["code"].(float64) != a2a.CodeInvalidParams {
		t.Fatalf("unknown tool = %v", msg)
	}
	p.send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_peers"}}`)
	if res, isErr = toolResult(t, p.next()); isErr || len(res.Peers) != 2 {
		t.Fatalf("list_peers = %+v", res)
	}
}

func TestMCPServeHoldsLiveness(t *testing.T) {
	h := newFakeHub(t)
	c := newTestClient(t, h, "")
	p := startServer(t, c, true)
	deadline := time.Now().Add(5 * time.Second)
	for !c.LivenessConnected() {
		if time.Now().After(deadline) {
			t.Fatal("liveness never connected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = p.in.Close()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
	if c.LivenessConnected() {
		t.Fatal("liveness outlived Serve")
	}
}

func TestToolDefinitionBudget(t *testing.T) {
	data, err := ToolsJSON()
	if err != nil {
		t.Fatal(err)
	}
	// About 3.5 bytes per token for this JSON: 1,200 bytes is about 340 tokens.
	if len(data) > 1200 {
		t.Fatalf("tool definitions are %d bytes; keep them under 1200 (proposal 21)", len(data))
	}
	t.Logf("tools/list result: %d bytes", len(data))
}
