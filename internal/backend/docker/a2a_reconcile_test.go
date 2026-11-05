// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/backend"
	"enclave/internal/peergroup"
)

// otherEngine is a second lifecycle on the same host state whose engine sees
// none of the fixture's containers, like Podman next to Docker.
func (f *a2aFixture) otherEngine(id peergroup.Engine) (*PeerGroups, *fakeA2AEngine) {
	engine := newFakeA2AEngine(f.t, f.state)
	p := newPeerGroups(engine, f.home, f.state, f.p.cfg, peergroup.NewSnapshotPublisher(f.state, nil))
	p.engineID = id
	return p, engine
}

func (f *a2aFixture) twoMemberGroup() (*Join, *Join) {
	orch := f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	worker := f.join("worker-1", a2a.RoleWorker, "172.18.0.16/28")
	return orch, worker
}

func (f *a2aFixture) assertGroupIntact(want int) {
	f.t.Helper()
	rec := f.record()
	if len(rec.Members) != want {
		f.t.Fatalf("members = %+v, want %d", rec.Members, want)
	}
	for _, m := range rec.Members {
		if m.State != peergroup.MemberActive {
			f.t.Fatalf("member %s is %s", m.Name, m.State)
		}
	}
}

func TestJoinRecordsEngine(t *testing.T) {
	f := newA2AFixture(t)
	f.twoMemberGroup()
	rec := f.record()
	if rec.Engine == nil || *rec.Engine != f.p.engineID {
		t.Fatalf("record engine = %+v, want %+v", rec.Engine, f.p.engineID)
	}
}

func TestReconcileSkipsGroupOfAnotherEngine(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   peergroup.Engine
		want string
	}{
		{"other engine", peergroup.Engine{Name: "podman", Root: "/home/u/.local/share/containers/storage"}, "use --backend docker"},
		{"other daemon", peergroup.Engine{Name: "docker", Root: "/home/u/.local/share/docker"}, "another docker daemon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newA2AFixture(t)
			_, worker := f.twoMemberGroup()
			table := peergroup.ContextTableDir(worker.spec.ConfigStoreDir, worker.generation)
			if err := os.MkdirAll(table, 0o700); err != nil {
				t.Fatal(err)
			}
			other, otherEngine := f.otherEngine(tc.id)

			results, err := other.ReconcileAll(context.Background())
			if err != nil || len(results) != 1 {
				t.Fatalf("ReconcileAll() = %+v, %v", results, err)
			}
			res := results[0]
			if !strings.Contains(res.Skipped, tc.want) || len(res.Actions) != 0 || res.Degraded {
				t.Fatalf("ReconcileAll() result = %+v, want skipped with %q", res, tc.want)
			}
			if len(otherEngine.calls) != 0 {
				t.Fatalf("the other engine was driven: %v", otherEngine.calls)
			}
			f.assertGroupIntact(2)
			if _, err := os.Stat(table); err != nil {
				t.Fatalf("worker context table deleted: %v", err)
			}
		})
	}
}

func TestLifecycleRefusesGroupOfAnotherEngine(t *testing.T) {
	f := newA2AFixture(t)
	f.twoMemberGroup()
	other, otherEngine := f.otherEngine(peergroup.Engine{Name: "podman"})
	ctx := context.Background()

	if _, err := other.Join(ctx, f.spec("worker-2", a2a.RoleWorker)); !IsForeignEngine(err) || !strings.Contains(err.Error(), "--backend docker") {
		t.Fatalf("Join() error = %v, want a foreign engine error naming --backend docker", err)
	}
	if _, err := other.Leave(ctx, LeaveSpec{Group: "review", Member: "worker-1"}); !IsForeignEngine(err) {
		t.Fatalf("Leave() error = %v, want a foreign engine error", err)
	}
	if _, _, err := other.IssueHostCredential(ctx, "review", time.Minute); !IsForeignEngine(err) || strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("IssueHostCredential() error = %v, want a foreign engine error without a reconcile hint", err)
	}
	if err := other.RevokeHostCredential(ctx, "review", "sha256:x"); !IsForeignEngine(err) {
		t.Fatalf("RevokeHostCredential() error = %v, want a foreign engine error", err)
	}
	if len(otherEngine.calls) != 0 {
		t.Fatalf("the other engine was driven: %v", otherEngine.calls)
	}
	f.assertGroupIntact(2)
	if len(f.record().Credentials) != 0 {
		t.Fatal("a credential was issued on the wrong engine")
	}
}

func TestHostCredentialHintNamesTheEngine(t *testing.T) {
	f := newA2AFixture(t)
	f.twoMemberGroup()
	f.engine.hub.Running = false
	_, _, err := f.p.IssueHostCredential(context.Background(), "review", time.Minute)
	if err == nil || !strings.Contains(err.Error(), "enclave a2a reconcile --peer-group review --backend docker") {
		t.Fatalf("IssueHostCredential() error = %v, want a reconcile hint naming the engine", err)
	}
}

func TestReconcileOfRecordWithoutEngine(t *testing.T) {
	f := newA2AFixture(t)
	f.twoMemberGroup()
	rec := f.record()
	rec.Engine = nil
	if err := f.p.store.Save(rec); err != nil {
		t.Fatal(err)
	}

	// Nothing of the group runs on the other engine: left alone and reported.
	other, otherEngine := f.otherEngine(peergroup.Engine{Name: "podman"})
	res, err := other.Reconcile(context.Background(), "review")
	if err != nil || !strings.Contains(res.Skipped, "records no engine") || len(otherEngine.calls) != 0 {
		t.Fatalf("Reconcile() on the other engine = %+v, %v (calls %v)", res, err, otherEngine.calls)
	}
	f.assertGroupIntact(2)
	if f.record().Engine != nil {
		t.Fatal("the other engine claimed the group")
	}

	// The hub of this group generation runs here: the record adopts this engine.
	res, err = f.p.Reconcile(context.Background(), "review")
	if err != nil || res.Skipped != "" || len(res.Actions) != 0 {
		t.Fatalf("Reconcile() = %+v, %v", res, err)
	}
	if rec := f.record(); rec.Engine == nil || *rec.Engine != f.p.engineID {
		t.Fatalf("record engine after reconcile = %+v", rec.Engine)
	}
	f.assertGroupIntact(2)
}

func writeRestrictions(t *testing.T, rec *peergroup.GroupRecord, j *Join, quarantine *a2aagent.Quarantine) {
	t.Helper()
	dir := peergroup.ContextTableDir(j.spec.ConfigStoreDir, j.generation)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(a2aagent.RestrictionsFileData{
		TableHeader: a2aagent.TableHeader{
			SchemaVersion: peergroup.SchemaVersion, Group: rec.Group, GroupGeneration: string(rec.GroupGeneration),
			Worker: j.spec.Member, WorkerGeneration: string(j.generation), UpdatedAt: peergroup.FormatTimestamp(time.Now()),
		},
		Quarantine: quarantine,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, a2aagent.RestrictionsFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileReleasesQuarantineWithoutObligation(t *testing.T) {
	f := newA2AFixture(t)
	_, worker := f.twoMemberGroup()
	// A cancel whose termination was unconfirmed: the worker quarantined itself, the record has no obligation.
	quarantine := &a2aagent.Quarantine{Since: peergroup.FormatTimestamp(time.Now()), Reason: "cancel"}
	writeRestrictions(t, f.record(), worker, quarantine)
	alive := true
	released := 0
	f.engine.signal = func(ref backend.SessionRef, req a2a.CtlRequest) (backend.WorkerSignalResult, error) {
		if req.Op != a2a.CtlReleaseQuarantine || ref.Name != worker.spec.SessionContainer {
			t.Fatalf("ctl %s to %s", req.Op, ref.Name)
		}
		value := alive
		if !alive {
			// The adapter clears its table before it answers.
			released++
			writeRestrictions(t, f.record(), worker, nil)
		}
		return backend.WorkerSignalResult{Response: a2a.CtlResponse{Op: req.Op, OK: !value, DescendantsAlive: &value}}, nil
	}

	// A descendant survives: the quarantine stays and becomes an obligation.
	res, err := f.p.Reconcile(context.Background(), "review")
	if err != nil || !res.Degraded || len(res.Problems) != 1 || !strings.Contains(res.Problems[0], "worker-1 stays quarantined") {
		t.Fatalf("Reconcile() = %+v, %v", res, err)
	}
	rec := f.record()
	if !hasObligation(rec, peergroup.ObligationQuarantineRelease) || len(rec.Releases) != 0 || rec.Obligations[0].Since == "" {
		t.Fatalf("record = %+v", rec)
	}

	// No descendant left: released through the obligation.
	alive = false
	res, err = f.p.Reconcile(context.Background(), "review")
	if err != nil || res.Degraded || !hasAction(res, "released quarantine of worker-1") {
		t.Fatalf("Reconcile() = %+v, %v", res, err)
	}
	rec = f.record()
	if len(rec.Obligations) != 0 || len(rec.Releases) != 1 || rec.Releases[0].Kind != "quarantine" || released != 1 {
		t.Fatalf("record = %+v, released %d", rec, released)
	}

	// Nothing left to release.
	f.engine.calls = nil
	res, err = f.p.Reconcile(context.Background(), "review")
	if err != nil || len(res.Actions) != 0 || hasCallWithPrefix(f.engine.calls, "SignalWorker") {
		t.Fatalf("Reconcile() after the release = %+v, %v (calls %v)", res, err, f.engine.calls)
	}

	// A quarantine without any obligation in the first place is released directly.
	writeRestrictions(t, f.record(), worker, quarantine)
	res, err = f.p.Reconcile(context.Background(), "review")
	if err != nil || !hasAction(res, "released quarantine of worker-1") || released != 2 {
		t.Fatalf("Reconcile() without obligation = %+v, %v", res, err)
	}
}

func TestReconcileLeavesQuarantineOfStoppedWorker(t *testing.T) {
	f := newA2AFixture(t)
	_, worker := f.twoMemberGroup()
	writeRestrictions(t, f.record(), worker, &a2aagent.Quarantine{Reason: "deadline"})
	f.engine.setSession(worker.spec.SessionContainer, memberSessionState{Exists: true, Ended: true, Labels: map[string]string{peergroup.LabelMemberGeneration: string(worker.generation)}})
	res, err := f.p.Reconcile(context.Background(), "review")
	if err != nil || res.Degraded || len(res.Actions) != 0 || hasCallWithPrefix(f.engine.calls, "SignalWorker") {
		t.Fatalf("Reconcile() = %+v, %v (calls %v)", res, err, f.engine.calls)
	}
	// B4: a stopped worker stays a member and keeps the hub (proposal 15).
	f.assertGroupIntact(2)
}

func TestReconcileTreatsExitedOrchestratorAsDeparture(t *testing.T) {
	f := newA2AFixture(t)
	orch, worker := f.twoMemberGroup()
	f.engine.setSession(orch.spec.SessionContainer, memberSessionState{Exists: true, Ended: true, Labels: map[string]string{peergroup.LabelMemberGeneration: string(orch.generation)}})
	f.engine.signal = func(ref backend.SessionRef, req a2a.CtlRequest) (backend.WorkerSignalResult, error) {
		if req.Op != a2a.CtlDepart || !req.Authoritative || req.Principal.Member != "orch" {
			t.Fatalf("ctl request = %+v", req)
		}
		return backend.WorkerSignalResult{Response: a2a.CtlResponse{Op: req.Op, OK: true, Departure: &a2a.DepartureAck{
			Worker: worker.spec.Member, WorkerGeneration: string(worker.generation), Principal: req.Principal,
			AdmissionStopPersisted: true, Termination: a2a.TerminationConfirmed,
		}}}, nil
	}
	res, err := f.p.Reconcile(context.Background(), "review")
	if err != nil || res.Degraded || !hasAction(res, "removed member orch: session container has exited") {
		t.Fatalf("Reconcile() = %+v, %v", res, err)
	}
	rec := f.record()
	if len(rec.Members) != 1 || rec.Members[0].Name != "worker-1" {
		t.Fatalf("members = %+v", rec.Members)
	}
	if len(rec.Departures) != 1 || rec.Departures[0].Kind != peergroup.DepartureAuthoritative || rec.Departures[0].State != peergroup.DepartureComplete {
		t.Fatalf("departures = %+v", rec.Departures)
	}

	// A created orchestrator container that has not run yet is no departure.
	f2 := newA2AFixture(t)
	orch2, _ := f2.twoMemberGroup()
	f2.engine.setSession(orch2.spec.SessionContainer, memberSessionState{Exists: true, Labels: map[string]string{peergroup.LabelMemberGeneration: string(orch2.generation)}})
	if res, err := f2.p.Reconcile(context.Background(), "review"); err != nil || len(res.Actions) != 0 {
		t.Fatalf("Reconcile() with a created container = %+v, %v", res, err)
	}
	f2.assertGroupIntact(2)
}

// joinUntilStarted runs a foreground join up to Started without creating the
// session container, as the foreground path does.
func (f *a2aFixture) joinUntilStarted(member string, role a2a.Role, subnet string) *Join {
	f.t.Helper()
	j, err := f.p.Join(context.Background(), f.spec(member, role))
	if err != nil {
		f.t.Fatal(err)
	}
	ctx := context.Background()
	if err := j.beforeGateway(ctx, "net-"+member, subnet); err != nil {
		f.t.Fatal(err)
	}
	if err := j.afterGateway(ctx, "172.18.0.34"); err != nil {
		f.t.Fatal(err)
	}
	j.Started()
	return j
}

func TestStartedHoldsGroupLockUntilSessionContainerExists(t *testing.T) {
	f := newA2AFixture(t)
	previous := reconcileLockWait
	reconcileLockWait = 100 * time.Millisecond
	t.Cleanup(func() { reconcileLockWait = previous })
	f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	j := f.joinUntilStarted("worker-1", a2a.RoleWorker, "172.18.0.32/28")

	// The member is active and its container does not exist yet: reconcile must not see that.
	res, err := f.p.Reconcile(context.Background(), "review")
	if err != nil || !res.Busy {
		t.Fatalf("Reconcile() before the container exists = %+v, %v", res, err)
	}
	f.engine.setSession(j.spec.SessionContainer, memberSessionState{Exists: true, Running: true, Labels: map[string]string{peergroup.LabelMemberGeneration: string(j.generation)}})
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err = f.p.Reconcile(context.Background(), "review")
		if err != nil || !res.Busy || time.Now().After(deadline) {
			break
		}
	}
	if err != nil || res.Busy || len(res.Actions) != 0 {
		t.Fatalf("Reconcile() after the container exists = %+v, %v", res, err)
	}
	f.assertGroupIntact(2)
}

func TestLeaveOnExitEndsStartedLockHold(t *testing.T) {
	f := newA2AFixture(t)
	f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	j := f.joinUntilStarted("worker-1", a2a.RoleWorker, "172.18.0.32/28")
	done := make(chan struct{})
	go func() {
		defer close(done)
		j.LeaveOnExit(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("LeaveOnExit waited for the lock hold of Started")
	}
	if res := j.LeaveResult(); res == nil || !res.Left {
		t.Fatalf("LeaveResult() = %+v", res)
	}
	f.assertGroupIntact(1)
}

func hasAction(res ReconcileResult, want string) bool {
	for _, action := range res.Actions {
		if action == want {
			return true
		}
	}
	return false
}
