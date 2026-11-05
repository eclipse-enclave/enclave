// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
)

const testGen = "0123456789abcdef"

// fakeTaskHub answers GetTask and CancelTask with a fixed state per task ID.
func fakeTaskHub(t *testing.T, states map[string]a2a.TaskState) a2aagent.Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == a2a.HubPeersPath {
			_ = json.NewEncoder(w).Encode(a2a.PeerListing{Group: "g", Peers: []a2a.PeerEntry{{Name: "builder", Generation: testGen}}})
			return
		}
		body, _ := io.ReadAll(r.Body)
		req, rpcErr := a2a.DecodeRequest(body)
		resp := a2a.Response{JSONRPC: "2.0", ID: req.ID, Error: rpcErr}
		if rpcErr == nil {
			var params a2a.GetTaskRequest
			_ = a2a.DecodeParams(req, &params)
			state := states[params.ID]
			if req.Method == a2a.MethodCancelTask {
				state = a2a.TaskStateCanceled
			}
			// GetTask and CancelTask answer a bare Task, as the adapter does.
			raw, _ := json.Marshal(a2a.Task{ID: params.ID, ContextID: "c1", Status: a2a.TaskStatus{State: state}})
			resp.Result = raw
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	p, _ := strconv.Atoi(port)
	return a2aagent.Config{
		Role: a2a.RoleOrchestrator, Group: "g", GroupGeneration: testGen, Member: "lead",
		MemberGeneration: testGen, HubHost: host, HubPort: p, InboundToken: "tok", TableDir: t.TempDir(),
	}
}

func runClientTest(t *testing.T, cfg a2aagent.Config, args ...string) (int, string, string) {
	t.Helper()
	old := loadConfig
	loadConfig = func([]string) (a2aagent.Config, error) { return cfg, nil }
	t.Cleanup(func() { loadConfig = old })
	var stdout, stderr bytes.Buffer
	code := clientMain(context.Background(), args, nil, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestClientExitCodes(t *testing.T) {
	cfg := fakeTaskHub(t, map[string]a2a.TaskState{
		"done": a2a.TaskStateCompleted, "busy": a2a.TaskStateWorking, "bad": a2a.TaskStateFailed,
	})
	handle := func(id string) string { return "task:builder:" + testGen + ":" + id }
	cases := []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"get", handle("done")}, 0, `"state":"TASK_STATE_COMPLETED"`},
		{[]string{"get", "--wait", "0s", handle("busy")}, 4, `"handle":"` + handle("busy") + `"`},
		{[]string{"get", handle("bad")}, 5, `"state":"TASK_STATE_FAILED"`},
		{[]string{"cancel", handle("busy")}, 0, `"state":"TASK_STATE_CANCELED"`},
		{[]string{"get", "not-a-handle"}, 1, `not Claude task IDs`},
		{[]string{"get"}, 0, `"note":"No sends recorded`},
	}
	for _, tc := range cases {
		code, out, errOut := runClientTest(t, cfg, tc.args...)
		if code != tc.code || !strings.Contains(out, tc.out) {
			t.Errorf("%v: exit %d out %q err %q; want exit %d with %q", tc.args, code, out, errOut, tc.code, tc.out)
		}
	}
}

func TestClientUsage(t *testing.T) {
	cfg := fakeTaskHub(t, nil)
	for _, args := range [][]string{{}, {"frobnicate"}, {"send", "builder"}, {"cancel"}, {"get", "--wait", "-1s", "x"}, {"peers", "extra"}} {
		if code, _, _ := runClientTest(t, cfg, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	cfg.TableDir = ""
	code, _, errOut := runClientTest(t, cfg, "get")
	if code != 1 || !strings.Contains(errOut, "--journal-dir") {
		t.Errorf("missing journal dir: exit %d err %q", code, errOut)
	}
	if code, _, _ := runClientTest(t, cfg, "--journal-dir", t.TempDir(), "get"); code != 0 {
		t.Errorf("--journal-dir: exit %d", code)
	}
}
