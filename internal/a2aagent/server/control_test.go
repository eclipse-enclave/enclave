// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux

package server

import (
	"slices"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/a2aagent/executor/fakedriver"
)

const secondEpoch = "2222222222222222"

func immediate(id string) a2a.SendMessageRequest {
	return a2a.SendMessageRequest{Message: message(id, id), Configuration: &a2a.SendMessageConfiguration{ReturnImmediately: true}}
}

func (e *env) taskState(id string) a2a.Task {
	e.t.Helper()
	return task(e.t, e.call(a2a.MethodGetTask, map[string]string{"id": id}, request{}))
}

func TestControlHandshake(t *testing.T) {
	fake := &fakeExec{}
	e := newEnv(t, envSetup{fake: fake})
	e.waitConnected()
	e.hub.mu.Lock()
	auth := e.hub.authSeen[0]
	e.hub.mu.Unlock()
	if auth != "Bearer inbound-token" {
		t.Fatalf("control stream authorization = %q", auth)
	}
	if got := fake.snapshot().epochs; !slices.Equal(got, []string{testEpoch}) {
		t.Fatalf("EpochChanged calls = %v", got)
	}
}

func TestHelloFromAnotherGroupGenerationIsRefused(t *testing.T) {
	fake := &fakeExec{}
	e := newEnv(t, envSetup{
		fake:  fake,
		tweak: func(o *Options) { o.EpochWait = 100 * time.Millisecond },
		hub:   func(h *fakeHub) { h.groupGen = "00000000000000ee" },
	})
	waitFor(t, "two refused connections", func() bool { return e.hub.connections() >= 2 })
	if st := e.srv.ControlStatus(); st != ControlConnecting {
		t.Fatalf("control status = %s, want connecting", st)
	}
	if len(fake.snapshot().epochs) != 0 {
		t.Fatalf("epoch accepted from a foreign hello")
	}
	wantError(t, e.call(a2a.MethodSendMessage, immediate("m1"), request{}), a2a.CodeEpochUnconfirmed, a2a.RefusalEpochUnconfirmed)
	if !strings.Contains(e.log.String(), "group generation") {
		t.Fatalf("log does not name the cause:\n%s", e.log.String())
	}
}

func TestSendWaitsForTheControlEpoch(t *testing.T) {
	e := newEnv(t, envSetup{
		fake:  &fakeExec{},
		tweak: func(o *Options) { o.EpochWait = 200 * time.Millisecond },
		hub:   func(h *fakeHub) { h.refuse = true },
	})
	wantError(t, e.call(a2a.MethodSendMessage, immediate("m1"), request{}), a2a.CodeEpochUnconfirmed, a2a.RefusalEpochUnconfirmed)
	if n := len(e.tbl.Tasks()); n != 0 {
		t.Fatalf("admitted %d task(s) without a confirmed epoch", n)
	}
	e.hub.set(func(h *fakeHub) { h.refuse = false })
	e.waitConnected()
	if r := result(t, e.call(a2a.MethodSendMessage, immediate("m1"), request{})); r.Task == nil {
		t.Fatalf("send after the handshake: %+v", r)
	}
	wantError(t, e.call(a2a.MethodSendMessage, immediate("m2"), request{epoch: secondEpoch}), a2a.CodeEpochUnconfirmed, a2a.RefusalEpochUnconfirmed)
}

func TestEpochChangeEndsOldEpochWork(t *testing.T) {
	e := newEnv(t, envSetup{mode: fakedriver.ModeHang, tweak: func(o *Options) { o.EpochWait = 300 * time.Millisecond }})
	e.waitConnected()
	running := result(t, e.call(a2a.MethodSendMessage, immediate("m1"), request{})).Task
	waitFor(t, "working", func() bool { return e.taskState(running.ID).Status.State == a2a.TaskStateWorking })
	queued := result(t, e.call(a2a.MethodSendMessage, immediate("m2"), request{})).Task

	e.hub.set(func(h *fakeHub) { h.epoch = secondEpoch })
	e.hub.kickAll()
	waitFor(t, "running task ended", func() bool { return e.taskState(running.ID).Status.State.Terminal() })
	for id, term := range map[string]a2a.Termination{running.ID: a2a.TerminationConfirmed, queued.ID: a2a.TerminationNotApplicable} {
		got := e.taskState(id)
		m := meta(t, got.Metadata)
		if got.Status.State != a2a.TaskStateFailed || m.Reason != a2a.ReasonEpochChanged || m.Termination != term {
			t.Fatalf("task %s: state %s, metadata %+v", id, got.Status.State, m)
		}
	}
	e.waitConnected()
	wantError(t, e.call(a2a.MethodSendMessage, immediate("m3"), request{}), a2a.CodeEpochUnconfirmed, a2a.RefusalEpochUnconfirmed)
	fresh := result(t, e.call(a2a.MethodSendMessage, immediate("m4"), request{epoch: secondEpoch})).Task
	waitFor(t, "new-epoch task runs", func() bool { return e.taskState(fresh.ID).Status.State == a2a.TaskStateWorking })
}

func TestKeepaliveLossReconnects(t *testing.T) {
	fake := &fakeExec{}
	e := newEnv(t, envSetup{fake: fake})
	e.waitConnected()
	start := time.Now()
	e.hub.set(func(h *fakeHub) { h.silent = true })
	waitFor(t, "reconnect after silence", func() bool { return e.hub.connections() >= 2 })
	if elapsed := time.Since(start); elapsed < 2*testKeepalive {
		t.Fatalf("reconnected after %s, before 2 keepalive intervals", elapsed)
	}
	e.hub.set(func(h *fakeHub) { h.silent = false })
	e.waitConnected()
	if !strings.Contains(e.log.String(), "keepalive missing for 2 intervals") {
		t.Fatalf("log does not report keepalive loss:\n%s", e.log.String())
	}
	snap := fake.snapshot()
	if snap.hubLost != 0 || !slices.Equal(snap.epochs, []string{testEpoch}) {
		t.Fatalf("a reconnect inside the window must not end work: %+v", snap)
	}
}

func TestHubLostAfterReconnectWindow(t *testing.T) {
	e := newEnv(t, envSetup{
		mode:     fakedriver.ModeHang,
		tweakCfg: func(c *a2aagent.Config) { c.ReconnectWindow = 400 * time.Millisecond },
		tweak:    func(o *Options) { o.EpochWait = 200 * time.Millisecond },
	})
	e.waitConnected()
	running := result(t, e.call(a2a.MethodSendMessage, immediate("m1"), request{})).Task
	waitFor(t, "working", func() bool { return e.taskState(running.ID).Status.State == a2a.TaskStateWorking })

	e.hub.set(func(h *fakeHub) { h.refuse = true })
	e.hub.kickAll()
	waitFor(t, "hub lost", func() bool { return e.srv.ControlStatus() == ControlHubLost })
	waitFor(t, "task ended", func() bool { return e.taskState(running.ID).Status.State.Terminal() })
	got := e.taskState(running.ID)
	if m := meta(t, got.Metadata); got.Status.State != a2a.TaskStateFailed || m.Reason != a2a.ReasonHubLost || m.Termination != a2a.TerminationConfirmed {
		t.Fatalf("hub-lost task: %s %+v", got.Status.State, m)
	}
	wantError(t, e.call(a2a.MethodSendMessage, immediate("m2"), request{}), a2a.CodeEpochUnconfirmed, a2a.RefusalEpochUnconfirmed)

	e.hub.set(func(h *fakeHub) { h.refuse = false })
	e.waitConnected()
	if r := result(t, e.call(a2a.MethodSendMessage, immediate("m3"), request{})); r.Task == nil {
		t.Fatalf("send after the hub returned: %+v", r)
	}
}

func TestSuspectedOrchestratorDeparture(t *testing.T) {
	e := newEnv(t, envSetup{
		mode:     fakedriver.ModeComplete,
		tweakCfg: func(c *a2aagent.Config) { c.ReconnectWindow = 300 * time.Millisecond },
	})
	e.waitConnected()
	flapping := orch
	flapping.Member = "orch-2"
	e.hub.pushOrchestrator(a2a.OrchestratorState{Member: flapping.Member, Generation: flapping.Generation, Connected: false})
	e.hub.pushOrchestrator(a2a.OrchestratorState{Member: flapping.Member, Generation: flapping.Generation, Connected: true})
	e.hub.pushOrchestrator(a2a.OrchestratorState{Member: orch.Member, Generation: orch.Generation, Connected: false})

	waitFor(t, "admission stop", func() bool {
		restr, _ := e.tbl.Restrictions()
		return len(restr.AdmissionStops) > 0
	})
	time.Sleep(400 * time.Millisecond)
	restr, _ := e.tbl.Restrictions()
	if len(restr.AdmissionStops) != 1 || restr.AdmissionStops[0].Principal != refOf(orch) || restr.AdmissionStops[0].Reason != "suspected" {
		t.Fatalf("admission stops: %+v", restr.AdmissionStops)
	}
	wantError(t, e.call(a2a.MethodSendMessage, immediate("m1"), request{}), a2a.CodeAdmissionStopped, "ADMISSION_STOPPED")
	// A later reconnect does not undo the stop; only the host releases it.
	e.hub.pushOrchestrator(a2a.OrchestratorState{Member: orch.Member, Generation: orch.Generation, Connected: true})
	time.Sleep(100 * time.Millisecond)
	wantError(t, e.call(a2a.MethodSendMessage, immediate("m2"), request{}), a2a.CodeAdmissionStopped, "ADMISSION_STOPPED")
	if r := result(t, e.call(a2a.MethodSendMessage, immediate("m3"), request{principal: &flapping})); r.Task == nil {
		t.Fatalf("reconnected orchestrator was refused: %+v", r)
	}
}

func TestSupersededOrchestratorGeneration(t *testing.T) {
	e := newEnv(t, envSetup{fake: &fakeExec{}})
	e.waitConnected()
	replaced := orch
	replaced.Generation = "0000000000000002"
	e.hub.pushMembership(a2a.ControlMembership{Revision: 2, Principals: []a2a.MemberPrincipal{
		{Member: orch.Member, Role: a2a.RoleOrchestrator, Generation: replaced.Generation},
	}})
	e.hub.pushOrchestrator(a2a.OrchestratorState{Member: orch.Member, Generation: replaced.Generation, Connected: true})
	waitFor(t, "generation recorded", func() bool { return !e.srv.orchestratorGenerationKnown(orch.Member, orch.Generation) })
	wantError(t, e.call(a2a.MethodGetTask, map[string]string{"id": "x"}, request{}), a2a.CodeGenerationUnknown, a2a.RefusalGenerationUnknown)
	wantError(t, e.call(a2a.MethodGetTask, map[string]string{"id": "x"}, request{principal: &replaced}), a2a.CodeTaskNotFound, "")
}

func TestPrincipalsMustBeCurrentMembers(t *testing.T) {
	e := newEnv(t, envSetup{fake: &fakeExec{}, tweak: func(o *Options) { o.EpochWait = 300 * time.Millisecond }})
	e.waitConnected()
	getTask := map[string]string{"id": "x"}
	stranger := orch
	stranger.Member = "orch-9"
	staleHost := host
	staleHost.Generation = "00000000000000bb"
	for name, p := range map[string]*a2a.Principal{"unlisted member": &stranger, "stale host generation": &staleHost} {
		t.Run(name, func(t *testing.T) {
			wantError(t, e.call(a2a.MethodGetTask, getTask, request{principal: p}), a2a.CodeGenerationUnknown, a2a.RefusalGenerationUnknown)
		})
	}
	wantError(t, e.call(a2a.MethodGetTask, getTask, request{principal: &host}), a2a.CodeTaskNotFound, "")

	// A request forwarded under a newer revision waits for that revision's membership.
	joined := append(defaultPrincipals(), a2a.MemberPrincipal{Member: stranger.Member, Role: a2a.RoleOrchestrator, Generation: stranger.Generation})
	go func() {
		time.Sleep(50 * time.Millisecond)
		e.hub.pushMembership(a2a.ControlMembership{Revision: 3, Principals: joined})
	}()
	ahead := request{principal: &stranger, headers: map[string]string{a2a.HeaderRevision: "3"}}
	wantError(t, e.call(a2a.MethodGetTask, getTask, ahead), a2a.CodeTaskNotFound, "")

	// An older revision arriving late does not undo the newer one.
	e.hub.pushMembership(a2a.ControlMembership{Revision: 2, Principals: defaultPrincipals()})
	wantError(t, e.call(a2a.MethodGetTask, getTask, request{principal: &stranger}), a2a.CodeTaskNotFound, "")

	// A removed principal is refused once the hub reports the revision.
	e.hub.pushMembership(a2a.ControlMembership{Revision: 4, Principals: defaultPrincipals()})
	waitFor(t, "removal applied", func() bool {
		return e.call(a2a.MethodGetTask, getTask, request{principal: &stranger}).Error.Code == a2a.CodeGenerationUnknown
	})
}

func TestRequestsBeforeAnyMembershipAreUnconfirmed(t *testing.T) {
	e := newEnv(t, envSetup{
		fake:  &fakeExec{},
		tweak: func(o *Options) { o.EpochWait = 100 * time.Millisecond },
		hub:   func(h *fakeHub) { h.refuse = true },
	})
	wantError(t, e.call(a2a.MethodGetTask, map[string]string{"id": "x"}, request{}), a2a.CodeEpochUnconfirmed, a2a.RefusalEpochUnconfirmed)
}
