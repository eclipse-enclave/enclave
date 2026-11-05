// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2ahub

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/peergroup"
)

func TestCallSendsCredentialAndBody(t *testing.T) {
	var got struct {
		method, path, auth, version, ext, ctype string
		body                                    string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.path, got.body = r.Method, r.URL.Path, string(b)
		got.auth, got.version, got.ext = r.Header.Get(a2a.HeaderAuthorization), r.Header.Get(a2a.HeaderVersion), r.Header.Get(a2a.HeaderExtensions)
		got.ctype = r.Header.Get("Content-Type")
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusUnauthorized)
		}
		_, _ = io.WriteString(w, "response")
	}))
	defer srv.Close()

	var out strings.Builder
	ok, err := Call(context.Background(), srv.URL, "/peers/worker-1", strings.NewReader("cred-123\n{\"jsonrpc\":\"2.0\"}\n"), &out)
	if err != nil || !ok {
		t.Fatalf("call: %v %v", ok, err)
	}
	if got.method != http.MethodPost || got.path != "/peers/worker-1" || got.auth != "Bearer cred-123" || got.body != "{\"jsonrpc\":\"2.0\"}\n" ||
		got.version != a2a.ProtocolVersion || got.ext != a2a.ExtensionURI || got.ctype != "application/json" || out.String() != "response" {
		t.Fatalf("request %+v out %q", got, out.String())
	}

	out.Reset()
	ok, err = Call(context.Background(), srv.URL, a2a.HubPeersPath, strings.NewReader("cred-123\r\n"), &out)
	if err != nil || !ok || got.method != http.MethodGet || got.auth != "Bearer cred-123" {
		t.Fatalf("GET call: %v %v %+v", ok, err, got)
	}
	ok, err = Call(context.Background(), srv.URL, "/fail", strings.NewReader("cred\n"), io.Discard)
	if err != nil || ok {
		t.Fatalf("non-200 call: %v %v", ok, err)
	}
	for _, path := range []string{"peers/x", "//evil.example/x", "http://evil.example/x"} {
		if _, err := Call(context.Background(), srv.URL, path, strings.NewReader("cred\n"), io.Discard); err == nil {
			t.Errorf("path %q accepted", path)
		}
	}
	if _, err := Call(context.Background(), srv.URL, "/x", strings.NewReader("\n{}"), io.Discard); err == nil {
		t.Error("empty credential accepted")
	}
}

func TestCallThroughProxyAsHost(t *testing.T) {
	f := newFixture(t, nil)
	adapter, rec := newAdapter(t, taskResult("t1", a2a.TaskStateCompleted))
	f.addWorker("worker-1", adapter)
	cred := f.addHostCredential(time.Minute)
	f.publishAndReload()
	var out strings.Builder
	ok, err := Call(context.Background(), f.hub.URL, "/peers/worker-1", strings.NewReader(cred+"\n"+sendBody(1, "hi")), &out)
	if err != nil || !ok || !strings.Contains(out.String(), `"t1"`) {
		t.Fatalf("call %v %v %s", ok, err, out.String())
	}
	if rec.last().header.Get(a2a.HeaderMember) != peergroup.HostMemberName {
		t.Fatal("not forwarded as the host principal")
	}
}

func TestOwnerCacheBounds(t *testing.T) {
	c := newOwnerCache(2)
	o := owner{member: "orch", generation: "1111111111111111"}
	gen := peergroup.Generation("a1b2c3d4e5f60718")
	for _, id := range []string{"t1", "t2", "t3"} {
		c.record("w", gen, id, o, taskSummary{state: a2a.TaskStateCompleted})
	}
	if _, ok := c.owner("w", gen, "t1"); ok {
		t.Fatal("oldest terminal summary not evicted")
	}
	if _, ok := c.owner("w", gen, "t3"); !ok {
		t.Fatal("newest terminal summary evicted")
	}
	// The first owner wins; a terminal state is final.
	c.record("w", gen, "t3", owner{member: "other"}, taskSummary{state: a2a.TaskStateWorking})
	if got, _ := c.owner("w", gen, "t3"); got != o {
		t.Fatalf("owner %+v", got)
	}
	if s, _ := c.summary("w", "t3"); s.state != a2a.TaskStateCompleted {
		t.Fatalf("summary %+v", s)
	}
	for i := 0; i < liveTasksPerWorker+10; i++ {
		c.record("w", gen, "live-"+strconv.Itoa(i), o, taskSummary{state: a2a.TaskStateWorking})
	}
	if n := len(c.workers["w"].tasks); n > liveTasksPerWorker+2 {
		t.Fatalf("%d cached tasks", n)
	}
	// Another generation of the worker never sees the old entries.
	if _, ok := c.owner("w", "bbbbbbbbbbbbbbbb", "t3"); ok {
		t.Fatal("entry leaked across worker generations")
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{
		a2a.EnvGroup:              "review",
		a2a.EnvGroupGeneration:    string(testGroupGen),
		a2a.EnvHubPort:            "41830",
		a2a.EnvHubLogMode:         "full",
		a2a.EnvHubRetainedResults: "7",
		a2a.EnvReconnectWindow:    "30s",
		a2a.EnvHubProxyUID:        "65532",
		a2a.EnvMaxRequestBytes:    "2048",
		a2a.EnvHubEpoch:           "abcdef0123456789",
	}
	cfg, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 41830 || cfg.LogMode != LogFull || cfg.RetainedResults != 7 || cfg.ReconnectWindow != 30*time.Second ||
		cfg.ProxyUID != 65532 || cfg.Epoch != "abcdef0123456789" || cfg.MaxRequestBytes != 2048 || cfg.SnapshotDir != peergroup.HubSnapshotMountPath ||
		cfg.StatusDir != peergroup.HubStatusMountPath || cfg.AuditDir != peergroup.HubAuditMountPath {
		t.Fatalf("config %+v", cfg)
	}
	for key, bad := range map[string]string{
		a2a.EnvGroup:              "Bad Name",
		a2a.EnvGroupGeneration:    "xyz",
		a2a.EnvHubPort:            "0",
		a2a.EnvHubLogMode:         "verbose",
		a2a.EnvHubRetainedResults: "-1",
		a2a.EnvReconnectWindow:    "soon",
		a2a.EnvHubEpoch:           "ABC",
	} {
		saved := env[key]
		env[key] = bad
		if _, err := ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
			t.Errorf("%s=%q accepted", key, bad)
		}
		env[key] = saved
	}
}
