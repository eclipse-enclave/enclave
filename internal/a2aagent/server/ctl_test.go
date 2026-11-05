// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux

package server

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/a2aagent/executor/fakedriver"
)

func (e *env) ctl(req a2a.CtlRequest) a2a.CtlResponse {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := CallCtl(ctx, e.socket, req)
	if err != nil {
		e.t.Fatalf("ctl %s: %v", req.Op, err)
	}
	if resp.Op != req.Op {
		e.t.Fatalf("ctl response op = %q, want %q", resp.Op, req.Op)
	}
	return resp
}

func TestCtlOperations(t *testing.T) {
	e := newEnv(t, envSetup{mode: fakedriver.ModeHang})
	e.waitConnected()
	running := result(t, e.call(a2a.MethodSendMessage, immediate("m1"), request{})).Task
	waitFor(t, "working", func() bool { return e.taskState(running.ID).Status.State == a2a.TaskStateWorking })
	queued := result(t, e.call(a2a.MethodSendMessage, immediate("m2"), request{})).Task
	hostTask := result(t, e.call(a2a.MethodSendMessage, immediate("h1"), request{principal: &host})).Task

	status := e.ctl(a2a.CtlRequest{Op: a2a.CtlStatus})
	if !status.OK || status.Quarantined == nil || *status.Quarantined || status.DescendantsAlive == nil || !*status.DescendantsAlive {
		t.Fatalf("status while running: %+v", status)
	}

	depart := e.ctl(a2a.CtlRequest{Op: a2a.CtlDepart, Principal: orch, Authoritative: true})
	ack := depart.Departure
	if !depart.OK || ack == nil {
		t.Fatalf("depart: %+v", depart)
	}
	if ack.Worker != "worker-1" || ack.WorkerGeneration != testMemberGeneration || ack.Principal != orch ||
		!ack.AdmissionStopPersisted || !slices.Equal(ack.QueuedDiscarded, []string{queued.ID}) ||
		!slices.Equal(ack.RunningTerminated, []string{running.ID}) || ack.Termination != a2a.TerminationConfirmed || ack.At == "" {
		t.Fatalf("departure ack: %+v", ack)
	}
	for id, term := range map[string]a2a.Termination{running.ID: a2a.TerminationConfirmed, queued.ID: a2a.TerminationNotApplicable} {
		got := e.taskState(id)
		if m := meta(t, got.Metadata); got.Status.State != a2a.TaskStateFailed || m.Reason != a2a.ReasonOrchestratorLost || m.Termination != term {
			t.Fatalf("task %s after departure: %s %+v", id, got.Status.State, m)
		}
	}
	wantError(t, e.call(a2a.MethodSendMessage, immediate("m3"), request{}), a2a.CodeAdmissionStopped, "ADMISSION_STOPPED")
	// The host principal's work is untouched and now runs.
	waitFor(t, "host task runs", func() bool {
		return task(t, e.call(a2a.MethodGetTask, map[string]string{"id": hostTask.ID}, request{principal: &host})).Status.State == a2a.TaskStateWorking
	})
	if _, err := e.ex.Cancel(context.Background(), hostTask.ID); err != nil {
		t.Fatalf("cancel host task: %v", err)
	}

	if r := e.ctl(a2a.CtlRequest{Op: a2a.CtlReleaseAdmission, Principal: orch}); !r.OK {
		t.Fatalf("release-admission: %+v", r)
	}

	if err := e.tbl.SetQuarantine(&a2aagent.Quarantine{Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	wantError(t, e.call(a2a.MethodSendMessage, immediate("m4"), request{}), a2a.CodeQuarantined, "QUARANTINED")
	if r := e.ctl(a2a.CtlRequest{Op: a2a.CtlStatus}); r.Quarantined == nil || !*r.Quarantined {
		t.Fatalf("status while quarantined: %+v", r)
	}
	released := e.ctl(a2a.CtlRequest{Op: a2a.CtlReleaseQuarantine})
	if !released.OK || released.Quarantined == nil || *released.Quarantined || released.DescendantsAlive == nil || *released.DescendantsAlive {
		t.Fatalf("release-quarantine with no descendants: %+v", released)
	}

	again := result(t, e.call(a2a.MethodSendMessage, immediate("m5"), request{})).Task
	waitFor(t, "working", func() bool { return e.taskState(again.ID).Status.State == a2a.TaskStateWorking })
	if err := e.tbl.SetQuarantine(&a2aagent.Quarantine{Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	kept := e.ctl(a2a.CtlRequest{Op: a2a.CtlReleaseQuarantine})
	if kept.OK || kept.Quarantined == nil || !*kept.Quarantined || kept.DescendantsAlive == nil || !*kept.DescendantsAlive || kept.Error == "" {
		t.Fatalf("release-quarantine with a live descendant: %+v", kept)
	}
}

func TestCtlRefusals(t *testing.T) {
	e := newEnv(t, envSetup{fake: &fakeExec{}})
	foreign := orch
	foreign.GroupGeneration = "00000000000000ee"
	worker := orch
	worker.Role = a2a.RoleWorker
	noGen := orch
	noGen.Generation = ""
	cases := map[string]a2a.CtlRequest{
		"unknown op":       {Op: "reboot"},
		"foreign group":    {Op: a2a.CtlDepart, Principal: foreign},
		"worker principal": {Op: a2a.CtlDepart, Principal: worker},
		"no generation":    {Op: a2a.CtlReleaseAdmission, Principal: noGen},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := CallCtl(context.Background(), e.socket, req)
			if err != nil || r.OK || r.Error == "" || r.Op != req.Op {
				t.Fatalf("want a refusal, got %+v, %v", r, err)
			}
		})
	}
	t.Run("malformed request", func(t *testing.T) {
		conn, err := net.Dial("unix", e.socket)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write([]byte("not json\n"))
		var resp a2a.CtlResponse
		if err := json.NewDecoder(conn).Decode(&resp); err != nil || resp.OK || !strings.Contains(resp.Error, "invalid") {
			t.Fatalf("malformed request: %+v, %v", resp, err)
		}
	})
}

func TestCtlSuspectedDepartureViaFakeExecutor(t *testing.T) {
	fake := &fakeExec{alive: true}
	e := newEnv(t, envSetup{fake: fake})
	if r := e.ctl(a2a.CtlRequest{Op: a2a.CtlDepart, Principal: host}); !r.OK || r.Departure == nil {
		t.Fatalf("depart: %+v", r)
	}
	if got := fake.snapshot().departs; len(got) != 1 || got[0].authoritative || got[0].ref != refOf(host) {
		t.Fatalf("Depart calls: %+v", got)
	}
}
