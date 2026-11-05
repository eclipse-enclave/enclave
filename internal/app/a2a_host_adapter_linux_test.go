// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux

package app

import (
	"context"
	"io"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent/executor/fakedriver"
	"enclave/internal/a2aagent/mcp/adaptertest"
	"enclave/internal/a2aagent/server"
	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/peergroup"
)

// adapterHostAccess forwards host calls to a real adapter as the host principal.
type adapterHostAccess struct {
	adapter *adaptertest.Adapter
	host    a2a.Principal
}

func (a adapterHostAccess) Issue(context.Context, string, time.Duration) (peergroup.InvocationCredential, error) {
	return peergroup.InvocationCredential{Token: "tok", Hash: "sha256:tok"}, nil
}

func (a adapterHostAccess) Revoke(context.Context, string, string) error { return nil }

func (a adapterHostAccess) Call(ctx context.Context, _, _, _ string, body []byte) (io.ReadCloser, error) {
	resp, err := a.adapter.Post(ctx, a.host, body)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// TestA2AHostTaskVerbsAgainstRealAdapter decodes the adapter's own GetTask and CancelTask responses.
func TestA2AHostTaskVerbsAgainstRealAdapter(t *testing.T) {
	f := newA2AFixture(t)
	host := a2a.Principal{Group: "review", GroupGeneration: string(a2aTestGroupGen), Member: server.HostMemberName,
		Role: a2a.RoleHost, Generation: "00000000000000aa"}
	access := adapterHostAccess{host: host, adapter: adaptertest.Start(t, adaptertest.Options{
		Group: host.Group, GroupGeneration: host.GroupGeneration,
		Worker: "worker-1", WorkerGeneration: string(a2aTestWorkerGen),
		Mode: fakedriver.ModeHang, Callers: []a2a.Principal{host},
	})}
	run := func(req *cli.A2ARequest) (int, a2aTaskOutput) {
		t.Helper()
		req.JSON = true
		code, text := runHostVerb(t, f, access, req, config.DefaultOptions())
		out := a2aTaskJSON(t, text)
		if out.Error != "" {
			t.Fatalf("%s: exit %d %s", req.Verb, code, text)
		}
		return code, out
	}

	_, sent := run(&cli.A2ARequest{Verb: cli.A2AVerbSend, Peer: "worker-1", Text: "hang", Detach: true})
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, got := run(&cli.A2ARequest{Verb: cli.A2AVerbTaskGet, Handle: sent.Handle})
		if got.State == string(a2a.TaskStateWorking) {
			if code != a2aExitWaitElapsed {
				t.Fatalf("get working: exit %d", code)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task stayed %s", got.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
	code, got := run(&cli.A2ARequest{Verb: cli.A2AVerbTaskCancel, Handle: sent.Handle})
	if code != 0 || got.State != string(a2a.TaskStateCanceled) || got.Termination != string(a2a.TerminationConfirmed) {
		t.Fatalf("cancel: exit %d %+v", code, got)
	}
	code, got = run(&cli.A2ARequest{Verb: cli.A2AVerbTaskGet, Handle: sent.Handle})
	if code != a2aExitTaskNotSuccess || got.State != string(a2a.TaskStateCanceled) || got.Reason != string(a2a.ReasonCancel) {
		t.Fatalf("get after cancel: exit %d %+v", code, got)
	}
}
