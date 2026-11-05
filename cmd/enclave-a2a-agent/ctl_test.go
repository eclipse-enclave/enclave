// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/a2a"
)

// stubAdapter answers every ctl connection with answer and records the requests.
func stubAdapter(t *testing.T, answer func(a2a.CtlRequest) a2a.CtlResponse) (string, *[]a2a.CtlRequest) {
	t.Helper()
	dir, err := os.MkdirTemp("", "a2a-ctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "ctl.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var seen []a2a.CtlRequest
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req a2a.CtlRequest
			if json.NewDecoder(conn).Decode(&req) == nil {
				seen = append(seen, req)
				_ = json.NewEncoder(conn).Encode(answer(req))
			}
			_ = conn.Close()
		}
	}()
	return socket, &seen
}

func TestCtl(t *testing.T) {
	socket, seen := stubAdapter(t, func(req a2a.CtlRequest) a2a.CtlResponse {
		return a2a.CtlResponse{Op: req.Op, OK: req.Op != a2a.CtlReleaseQuarantine, Error: "kept"}
	})
	principal := `"principal":{"group":"review","groupGeneration":"00000000000000ff","member":"orch","role":"orchestrator","generation":"0000000000000001"}`
	cases := []struct {
		name  string
		args  []string
		stdin string
		code  int
		op    string
	}{
		{"op on stdin", nil, `{"op":"depart","authoritative":true,` + principal + `}`, 0, a2a.CtlDepart},
		{"op on argv", []string{"status"}, `{}`, 0, a2a.CtlStatus},
		{"matching ops", []string{"release-admission"}, `{"op":"release-admission",` + principal + `}`, 0, a2a.CtlReleaseAdmission},
		{"not OK", []string{"release-quarantine"}, `{}`, 1, a2a.CtlReleaseQuarantine},
		{"contradicting ops", []string{"status"}, `{"op":"depart"}`, 2, ""},
		{"unknown op", nil, `{"op":"reboot"}`, 2, ""},
		{"not json", nil, `depart`, 2, ""},
		{"too many args", []string{"status", "extra"}, `{}`, 2, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(*seen)
			var out bytes.Buffer
			if code := ctl(tc.args, strings.NewReader(tc.stdin), &out, socket); code != tc.code {
				t.Fatalf("exit %d, want %d; stdout %s", code, tc.code, out.String())
			}
			if tc.code == 2 && len(tc.args) > 1 {
				return
			}
			var resp a2a.CtlResponse
			if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
				t.Fatalf("stdout is not a ctl response: %q", out.String())
			}
			if tc.op == "" {
				if len(*seen) != before || resp.Error == "" {
					t.Fatalf("a refused request reached the adapter or carries no error: %+v", resp)
				}
				return
			}
			if resp.Op != tc.op || len(*seen) != before+1 || (*seen)[before].Op != tc.op {
				t.Fatalf("response %+v, requests %+v", resp, *seen)
			}
		})
	}
	if got := (*seen)[0]; !got.Authoritative || got.Principal.Member != "orch" {
		t.Fatalf("request not passed through: %+v", got)
	}

	t.Run("adapter not running", func(t *testing.T) {
		var out bytes.Buffer
		if code := ctl([]string{"status"}, strings.NewReader(`{}`), &out, filepath.Join(t.TempDir(), "none.sock")); code != 1 {
			t.Fatalf("exit %d, want 1", code)
		}
		if !strings.Contains(out.String(), "adapter not reachable") {
			t.Fatalf("stdout %q", out.String())
		}
	})
}
