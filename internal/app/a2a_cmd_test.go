// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/peergroup"
)

// Regenerate with: go test ./internal/app/ -run A2A -update-a2a-golden
var updateA2AGolden = flag.Bool("update-a2a-golden", false, "rewrite the a2a golden files")

var a2aTestNow = time.Date(2026, 11, 5, 12, 0, 30, 0, time.UTC)

const (
	a2aTestGroupGen  = peergroup.Generation("3f9c0a1b2c3d4e5f")
	a2aTestWorkerGen = peergroup.Generation("a1b2c3d4e5f60718")
	a2aTestOrchGen   = peergroup.Generation("0f1e2d3c4b5a6978")
	a2aTestW2Gen     = peergroup.Generation("1122334455667788")
)

// a2aFixture is a group "review" with an orchestrator and two workers;
// worker-1 has a fresh table, worker-2 none.
type a2aFixture struct {
	home  string
	state a2aState
	rec   *peergroup.GroupRecord
	store map[string]string
}

func newA2AFixture(t *testing.T) *a2aFixture {
	t.Helper()
	home := t.TempDir()
	f := &a2aFixture{home: home, state: newA2AState(home), store: map[string]string{}}
	if _, err := f.state.store.Create("review"); err != nil {
		t.Fatal(err)
	}
	member := func(name string, role a2a.Role, gen peergroup.Generation, port int, hub string) peergroup.GroupMember {
		return peergroup.GroupMember{
			Name: name, Role: role, Generation: gen, State: peergroup.MemberActive,
			SessionContainer: "enclave-claude-ab12cd34ef56-" + name, Tool: "claude",
			ProjectHash: "ab12cd34ef56", ConfigStoreKey: "default", ProjectPath: "/home/u/src/app",
			MountMode: "rw", Network: "enclave-claude-ab12cd34ef56-" + name + "-net", Subnet: "172.18.0.0/28",
			HubAddress: hub, GatewayAddress: "172.18.0.2", AdapterPort: port,
			InboundTokenHash: "sha256:00", Description: name + " reviews diffs", JoinedAt: "2026-11-05T11:00:00Z",
		}
	}
	f.rec = &peergroup.GroupRecord{
		SchemaVersion:   "1",
		Group:           "review",
		GroupGeneration: a2aTestGroupGen,
		CreatedAt:       "2026-11-05T11:00:00Z",
		HubContainer:    "enclave-a2a-hub-review",
		HostPrincipal:   peergroup.HostPrincipal{Name: peergroup.HostMemberName, Generation: "0011223344556677"},
		Committed:       7,
		Members: []peergroup.GroupMember{
			member("worker-1", a2a.RoleWorker, a2aTestWorkerGen, peergroup.AdapterPort, "172.18.0.14"),
			member("lead", a2a.RoleOrchestrator, a2aTestOrchGen, 0, "172.18.0.30"),
			member("worker-2", a2a.RoleWorker, a2aTestW2Gen, peergroup.AdapterPort, "172.18.0.46"),
		},
		Departures:  []peergroup.Departure{},
		Obligations: []peergroup.Obligation{},
		Releases:    []peergroup.Release{},
		Credentials: []peergroup.InvocationRecord{},
	}
	f.saveRecord(t)
	for _, m := range f.rec.Members {
		dir := peergroup.ContextTableDir(filepath.Join(home, "store", m.Name), m.Generation)
		f.store[m.Name] = dir
		if err := f.state.inventory.Add("review", a2aTestGroupGen, peergroup.InventoryEntry{
			Member: m.Name, MemberGeneration: m.Generation, Tool: m.Tool, ProjectHash: m.ProjectHash,
			ConfigStoreKey: m.ConfigStoreKey, StorePath: dir, WrittenAt: "2026-11-05T11:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.writeMembership(t)
	f.writeRestrictions(t, "worker-1", a2aTestWorkerGen, "2026-11-05T12:00:00Z", nil, nil)
	return f
}

func (f *a2aFixture) saveRecord(t *testing.T) {
	t.Helper()
	data, err := json.MarshalIndent(f.rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(peergroup.GroupRecordPath(f.state.stateRoot, "review"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *a2aFixture) writeMembership(t *testing.T) {
	t.Helper()
	m := peergroup.Membership{SchemaVersion: "1", Group: "review", GroupGeneration: a2aTestGroupGen, Revision: f.rec.Committed, HubPort: peergroup.HubPort}
	for _, member := range f.rec.Members {
		pm := peergroup.MembershipMember{Name: member.Name, Role: member.Role, Generation: member.Generation, Description: member.Description}
		if member.Name == "worker-1" {
			pm.Skills = []a2a.AgentSkill{{ID: "review", Name: "Review", Description: "Reviews a diff", Tags: []string{"code"}}}
		}
		m.Members = append(m.Members, pm)
	}
	dir := peergroup.SnapshotDir(f.state.stateRoot, "review", f.rec.Committed)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, peergroup.MembershipFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *a2aFixture) writeRestrictions(t *testing.T, worker string, gen peergroup.Generation, updated string, stops []a2aagent.AdmissionStop, quarantine *a2aagent.Quarantine) {
	t.Helper()
	r := a2aagent.RestrictionsFileData{
		TableHeader:    a2aagent.TableHeader{SchemaVersion: "1", Group: "review", GroupGeneration: string(a2aTestGroupGen), Worker: worker, WorkerGeneration: string(gen), UpdatedAt: updated},
		AdmissionStops: stops,
		Quarantine:     quarantine,
	}
	if r.AdmissionStops == nil {
		r.AdmissionStops = []a2aagent.AdmissionStop{}
	}
	dir := f.store[worker]
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(r)
	if err := os.WriteFile(filepath.Join(dir, a2aagent.RestrictionsFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func checkA2AGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateA2AGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run once with -update-a2a-golden): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from golden:\n%s", name, got)
	}
}

// a2aTestIdentity is the label identity of a fixture member.
func a2aTestIdentity(member string, role a2a.Role, gen peergroup.Generation) *backend.SessionPeerGroup {
	return &backend.SessionPeerGroup{Group: "review", GroupGeneration: string(a2aTestGroupGen), Member: member, MemberGeneration: string(gen), Role: role}
}

func a2aTestSessions() []backend.Session {
	return []backend.Session{
		{Ref: backend.SessionRef{Name: "enclave-claude-ab12cd34ef56-worker-1"}, Tool: "claude", ProjectHash: "ab12cd34ef56", Status: "running", Name: "worker-1", Background: true, PeerGroup: a2aTestIdentity("worker-1", a2a.RoleWorker, a2aTestWorkerGen)},
		{Ref: backend.SessionRef{Name: "enclave-claude-ab12cd34ef56-lead"}, Tool: "claude", ProjectHash: "ab12cd34ef56", Status: "running", Name: "lead", PeerGroup: a2aTestIdentity("lead", a2a.RoleOrchestrator, a2aTestOrchGen)},
		{Ref: backend.SessionRef{Name: "enclave-claude-ab12cd34ef56-worker-2"}, Tool: "claude", ProjectHash: "ab12cd34ef56", Status: "running", Name: "worker-2", Background: true, PeerGroup: a2aTestIdentity("worker-2", a2a.RoleWorker, a2aTestW2Gen)},
		{Ref: backend.SessionRef{Name: "enclave-claude-ab12cd34ef56"}, Tool: "claude", ProjectHash: "ab12cd34ef56", Status: "running"},
	}
}

func TestPSJSONA2AObjectGolden(t *testing.T) {
	f := newA2AFixture(t)
	f.rec.Departures = []peergroup.Departure{{Member: "worker-2", Generation: a2aTestW2Gen, Kind: peergroup.DepartureSuspected, State: peergroup.DeparturePending, ObservedAt: "2026-11-05T11:50:00Z", UpdatedAt: "2026-11-05T11:55:00Z"}}
	f.saveRecord(t)
	f.writeRestrictions(t, "worker-1", a2aTestWorkerGen, "2026-11-05T12:00:00Z",
		[]a2aagent.AdmissionStop{{Principal: a2aagent.PrincipalRef{Member: "lead", Role: a2a.RoleOrchestrator, Generation: string(a2aTestOrchGen)}, Reason: "suspected", Since: "2026-11-05T11:59:00Z"}}, nil)

	sessions := a2aTestSessions()
	objects := collectPSA2AObjects(&f.state, sessions, a2aTestNow, 5*time.Minute)
	entries := buildPSJSONEntries(sessions)
	attachPSA2A(entries, objects)
	var out bytes.Buffer
	if err := renderPSJSONEntries(&out, entries); err != nil {
		t.Fatal(err)
	}
	checkA2AGolden(t, "a2a_ps.golden.json", out.Bytes())

	var decoded []map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if v, ok := decoded[0]["a2a"]; !ok || v != nil {
		t.Fatalf("session outside a group: a2a = %#v, want explicit null", v)
	}
}

// A labelled session whose record is gone, belongs to another group
// generation, or no longer lists its member generation keeps its identity.
func TestPSJSONA2AObjectWithoutRecord(t *testing.T) {
	f := newA2AFixture(t)
	gone := a2aTestIdentity("worker-9", a2a.RoleWorker, "9999999999999999")
	gone.Group = "gone"
	oldGroup := a2aTestIdentity("worker-1", a2a.RoleWorker, a2aTestWorkerGen)
	oldGroup.GroupGeneration = "0000000000000000"
	oldMember := a2aTestIdentity("worker-1", a2a.RoleWorker, "0000000000000001")
	sessions := []backend.Session{
		{Ref: backend.SessionRef{Name: "gone"}, PeerGroup: gone},
		{Ref: backend.SessionRef{Name: "old-group"}, PeerGroup: oldGroup},
		{Ref: backend.SessionRef{Name: "old-member"}, PeerGroup: oldMember},
		{Ref: backend.SessionRef{Name: "unlabelled-member"}},
	}
	for name, state := range map[string]*a2aState{"records": &f.state, "no home": nil} {
		t.Run(name, func(t *testing.T) {
			objects := collectPSA2AObjects(state, sessions, a2aTestNow, 5*time.Minute)
			if _, ok := objects["unlabelled-member"]; ok {
				t.Fatal("a session without labels got an a2a object")
			}
			for _, s := range sessions[:3] {
				obj := objects[s.Ref.Name]
				if obj == nil {
					t.Fatalf("%s: a2a is null", s.Ref.Name)
				}
				if obj.Group != s.PeerGroup.Group || obj.GroupGeneration != s.PeerGroup.GroupGeneration || obj.Generation != s.PeerGroup.MemberGeneration || obj.Role != "worker" {
					t.Fatalf("%s: identity = %+v", s.Ref.Name, obj)
				}
				if obj.Hub != nil || obj.Port != nil || obj.SnapshotRevision != nil {
					t.Fatalf("%s: record fields set: %+v", s.Ref.Name, obj)
				}
				for _, v := range []string{obj.Quarantine, obj.AdmissionStop, obj.Departure} {
					if v != a2aUnknown {
						t.Fatalf("%s: tri-state %q, want unknown", s.Ref.Name, v)
					}
				}
				if obj.QuarantineAt != nil || obj.AdmissionStopSource != nil || obj.DepartureAt != nil {
					t.Fatalf("%s: unknown fields carry a time or source: %+v", s.Ref.Name, obj)
				}
			}
		})
	}
}

func TestA2AStatusTriState(t *testing.T) {
	stop := func(member string, gen peergroup.Generation) []a2aagent.AdmissionStop {
		return []a2aagent.AdmissionStop{{Principal: a2aagent.PrincipalRef{Member: member, Generation: string(gen)}, Reason: "authoritative", Since: "2026-11-05T11:59:00Z"}}
	}
	cases := []struct {
		name   string
		setup  func(*testing.T, *a2aFixture)
		member string
		want   [3]string
	}{
		{"worker fresh table", nil, "worker-1", [3]string{"no", "no", "no"}},
		{"worker without table", nil, "worker-2", [3]string{"unknown", "unknown", "no"}},
		{"worker stale table", func(t *testing.T, f *a2aFixture) {
			f.writeRestrictions(t, "worker-1", a2aTestWorkerGen, "2026-11-05T11:50:00Z", nil, nil)
		}, "worker-1", [3]string{"unknown", "unknown", "no"}},
		{"worker table of another generation", func(t *testing.T, f *a2aFixture) {
			f.writeRestrictions(t, "worker-1", a2aTestW2Gen, "2026-11-05T12:00:00Z", nil, nil)
		}, "worker-1", [3]string{"unknown", "unknown", "no"}},
		{"worker quarantined and stopped", func(t *testing.T, f *a2aFixture) {
			f.writeRestrictions(t, "worker-1", a2aTestWorkerGen, "2026-11-05T12:00:00Z", stop("lead", a2aTestOrchGen), &a2aagent.Quarantine{Since: "2026-11-05T11:58:00Z", Reason: "termination unconfirmed"})
		}, "worker-1", [3]string{"yes", "yes", "no"}},
		{"orchestrator with a missing worker table", nil, "lead", [3]string{"no", "unknown", "no"}},
		{"orchestrator stopped by one worker", func(t *testing.T, f *a2aFixture) {
			f.writeRestrictions(t, "worker-1", a2aTestWorkerGen, "2026-11-05T12:00:00Z", stop("lead", a2aTestOrchGen), nil)
		}, "lead", [3]string{"no", "yes", "no"}},
		{"orchestrator stop of an older generation", func(t *testing.T, f *a2aFixture) {
			f.writeRestrictions(t, "worker-1", a2aTestWorkerGen, "2026-11-05T12:00:00Z", stop("lead", "ffffffffffffffff"), nil)
			f.writeRestrictions(t, "worker-2", a2aTestW2Gen, "2026-11-05T12:00:00Z", nil, nil)
		}, "lead", [3]string{"no", "no", "no"}},
		{"departure obligation", func(t *testing.T, f *a2aFixture) {
			f.rec.Obligations = []peergroup.Obligation{{Kind: peergroup.ObligationDepartureAck, Member: "lead", Generation: a2aTestOrchGen, Since: "2026-11-05T11:57:00Z"}}
			f.saveRecord(t)
		}, "lead", [3]string{"no", "unknown", "yes"}},
		{"complete departure is no", func(t *testing.T, f *a2aFixture) {
			f.rec.Departures = []peergroup.Departure{{Member: "worker-1", Generation: a2aTestWorkerGen, State: peergroup.DepartureComplete}}
			f.saveRecord(t)
		}, "worker-1", [3]string{"no", "no", "no"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newA2AFixture(t)
			if tc.setup != nil {
				tc.setup(t, f)
			}
			view, err := f.state.loadView("review", a2aTestNow, 5*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			var m peergroup.GroupMember
			for _, candidate := range view.record.Members {
				if candidate.Name == tc.member {
					m = candidate
				}
			}
			st := view.status(m)
			got := [3]string{st.Quarantine.Value, st.AdmissionStop.Value, st.Departure.Value}
			if got != tc.want {
				t.Fatalf("status = %v, want %v", got, tc.want)
			}
			for _, tri := range []a2aTri{st.Quarantine, st.AdmissionStop, st.Departure} {
				if (tri.Value == a2aUnknown) != (tri.Source == nil) || (tri.Value == a2aUnknown && tri.At != nil) {
					t.Fatalf("unknown must carry null at and source, known must carry a source: %+v", tri)
				}
			}
		})
	}
}

func TestA2APeersGolden(t *testing.T) {
	f := newA2AFixture(t)
	view, err := f.state.loadView("review", a2aTestNow, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writeA2AJSON(&out, buildA2APeers(view)); err != nil {
		t.Fatal(err)
	}
	checkA2AGolden(t, "a2a_peers.golden.json", out.Bytes())

	var table bytes.Buffer
	if err := renderA2APeersTable(&table, buildA2APeers(view)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), "lead") || !strings.HasPrefix(table.String(), "Peer group review") {
		t.Fatalf("table:\n%s", table.String())
	}
}

func TestA2AResolveGroup(t *testing.T) {
	f := newA2AFixture(t)
	if got, err := f.state.resolveGroup(""); err != nil || got != "review" {
		t.Fatalf("single group: %q, %v", got, err)
	}
	if _, err := f.state.store.Create("other"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.state.resolveGroup(""); err == nil || !strings.Contains(err.Error(), "--peer-group") {
		t.Fatalf("two groups: %v", err)
	}
	if _, err := f.state.resolveGroup("Bad_Name"); err == nil {
		t.Fatal("invalid group name accepted")
	}
}

func TestA2ALogTail(t *testing.T) {
	var out bytes.Buffer
	if err := copyA2ALog(&out, strings.NewReader("1\n2\n3\n"), 2); err != nil {
		t.Fatal(err)
	}
	if out.String() != "2\n3\n" {
		t.Fatalf("tail 2 = %q", out.String())
	}
	out.Reset()
	if err := copyA2ALog(&out, strings.NewReader("1\n2\n"), 0); err != nil || out.String() != "1\n2\n" {
		t.Fatalf("all = %q, %v", out.String(), err)
	}
}

type fakeLifecycle struct {
	calls      []string
	degraded   map[string]bool
	leaveError map[string]error
}

func (f *fakeLifecycle) Leave(_ context.Context, spec backenddocker.LeaveSpec) (backenddocker.LeaveResult, error) {
	f.calls = append(f.calls, "leave "+spec.Member+" "+string(spec.Generation))
	res := backenddocker.LeaveResult{Left: true}
	if f.degraded[spec.Member] {
		res.Degraded = true
		res.Problems = []string{"worker-1 unreachable"}
	}
	return res, f.leaveError[spec.Member]
}

func (f *fakeLifecycle) Reconcile(_ context.Context, group string) (backenddocker.ReconcileResult, error) {
	f.calls = append(f.calls, "reconcile "+group)
	return backenddocker.ReconcileResult{Group: group}, nil
}

func (f *fakeLifecycle) ReconcileAll(context.Context) ([]backenddocker.ReconcileResult, error) {
	f.calls = append(f.calls, "reconcile-all")
	return nil, nil
}

func TestStopPeerGroupLeavesOrchestratorFirst(t *testing.T) {
	f := newA2AFixture(t)
	lc := &fakeLifecycle{degraded: map[string]bool{"lead": true}}
	stopSession := func(_ context.Context, container string) error {
		lc.calls = append(lc.calls, "stop "+strings.TrimPrefix(container, "enclave-claude-ab12cd34ef56-"))
		return nil
	}
	res, err := stopPeerGroup(context.Background(), lc, f.state.store, "review", stopSession)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"leave lead " + string(a2aTestOrchGen), "stop lead",
		"leave worker-1 " + string(a2aTestWorkerGen), "stop worker-1",
		"leave worker-2 " + string(a2aTestW2Gen), "stop worker-2",
	}
	if !slices.Equal(lc.calls, want) {
		t.Fatalf("calls = %v, want %v", lc.calls, want)
	}
	if len(res.Stopped) != 3 || !res.Degraded || res.ExitCode() != a2aExitDegraded {
		t.Fatalf("result = %+v, exit %d", res, res.ExitCode())
	}

	clean := &fakeLifecycle{leaveError: map[string]error{"worker-2": errors.New("lock")}}
	res, err = stopPeerGroup(context.Background(), clean, f.state.store, "review", stopSession)
	if err == nil || !strings.Contains(err.Error(), "leave worker-2") || res.ExitCode() != 0 {
		t.Fatalf("leave error: %+v, %v", res, err)
	}
	if _, err := stopPeerGroup(context.Background(), clean, f.state.store, "missing", stopSession); err == nil {
		t.Fatal("missing group accepted")
	}
}

func TestReportA2AReconcileExitCodes(t *testing.T) {
	var out bytes.Buffer
	if code := reportA2AReconcile(&out, []backenddocker.ReconcileResult{{Group: "a"}, {Group: "b", Actions: []string{"committed revision 3"}}}, nil, false); code != 0 {
		t.Fatalf("clean: %d", code)
	}
	if code := reportA2AReconcile(&out, []backenddocker.ReconcileResult{{Group: "a", Degraded: true, Problems: []string{"x"}}}, nil, false); code != a2aExitDegraded {
		t.Fatalf("degraded: %d", code)
	}
	if code := reportA2AReconcile(&out, []backenddocker.ReconcileResult{{Group: "a", Busy: true}, {Group: "b", Degraded: true}}, nil, false); code != 1 {
		t.Fatalf("busy: %d", code)
	}
	if code := reportA2AReconcile(&out, nil, errors.New("engine"), false); code != 1 {
		t.Fatalf("error: %d", code)
	}
	if code := reportA2AReconcile(&out, nil, nil, true); code != backend.ExitCodeInterrupted {
		t.Fatalf("interrupted: %d", code)
	}
}

func TestPlanA2AHostOpLifetime(t *testing.T) {
	opts := config.DefaultOptions()
	wait := 30 * time.Second
	long := 20 * time.Minute
	cases := []struct {
		name     string
		verb     string
		detach   bool
		wait     *time.Duration
		lifetime time.Duration
		opWait   time.Duration
		err      bool
	}{
		{name: "blocking send", verb: "send", lifetime: 11 * time.Minute, opWait: 10 * time.Minute},
		{name: "send with wait", verb: "send", wait: &wait, lifetime: 90 * time.Second, opWait: wait},
		{name: "detach", verb: "send", detach: true, lifetime: 2 * time.Minute},
		{name: "get", verb: "task-get", lifetime: 2 * time.Minute},
		{name: "get with wait", verb: "task-get", wait: &wait, lifetime: 90 * time.Second, opWait: wait},
		{name: "cancel", verb: "task-cancel", lifetime: 2 * time.Minute},
		{name: "wait above cap", verb: "task-retry", wait: &long, err: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op, err := planA2AHostOp(opts, newA2ARequest(tc.verb, tc.detach, tc.wait), "review")
			if tc.err {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil || op.lifetime != tc.lifetime || op.wait != tc.opWait {
				t.Fatalf("op = %+v, %v", op, err)
			}
		})
	}
}

func TestRunStopPeerGroupExitCodes(t *testing.T) {
	f := newA2AFixture(t)
	lc := &fakeLifecycle{degraded: map[string]bool{"lead": true}}
	oldHome, oldLifecycle := a2aResolveHome, a2aNewLifecycle
	a2aResolveHome = func() (string, error) { return f.home, nil }
	a2aNewLifecycle = func(context.Context, model.Options, string) (a2aLifecycle, *backenddocker.Backend, error) {
		return lc, nil, nil
	}
	t.Cleanup(func() { a2aResolveHome, a2aNewLifecycle = oldHome, oldLifecycle })
	stopped := 0
	stop := func(context.Context, string) error { stopped++; return nil }
	// The reconcile after the stop resolves the degraded leave.
	if code := runStopPeerGroup(config.DefaultOptions(), "review", stop); code != 0 || stopped != 3 || lc.calls[len(lc.calls)-1] != "reconcile review" {
		t.Fatalf("degraded stop: exit %d, stopped %d, calls %v", code, stopped, lc.calls)
	}
	lc.degraded = nil
	if code := runStopPeerGroup(config.DefaultOptions(), "review", stop); code != 0 {
		t.Fatalf("clean stop: exit %d", code)
	}
	if code := runStopPeerGroup(config.DefaultOptions(), "missing", stop); code != 1 {
		t.Fatalf("missing group: exit %d", code)
	}
}

// TestRunStopPeerGroupExitReflectsStateAfterReconcile: stop --peer-group exits 3
// only when something is still degraded after its reconcile (CR-F5a-4).
func TestRunStopPeerGroupExitReflectsStateAfterReconcile(t *testing.T) {
	cases := []struct {
		name          string
		leaveDegraded bool
		lc            reconcilingLifecycle
		want          int
	}{
		{"degraded leave resolved by the reconcile", true, reconcilingLifecycle{}, 0},
		{"obligation still open after the reconcile", true, reconcilingLifecycle{open: true}, a2aExitDegraded},
		{"clean leave, reconcile finds another problem", false, reconcilingLifecycle{open: true}, a2aExitDegraded},
		{"group busy, degraded leave not retried", true, reconcilingLifecycle{busy: true}, a2aExitDegraded},
		{"group busy after a clean leave", false, reconcilingLifecycle{busy: true}, 0},
		{"reconcile failed", true, reconcilingLifecycle{err: errors.New("engine down")}, a2aExitDegraded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newA2AFixture(t)
			lc := tc.lc
			lc.fakeLifecycle = &fakeLifecycle{degraded: map[string]bool{"lead": tc.leaveDegraded}}
			oldHome, oldLifecycle := a2aResolveHome, a2aNewLifecycle
			a2aResolveHome = func() (string, error) { return f.home, nil }
			a2aNewLifecycle = func(context.Context, model.Options, string) (a2aLifecycle, *backenddocker.Backend, error) {
				return &lc, nil, nil
			}
			t.Cleanup(func() { a2aResolveHome, a2aNewLifecycle = oldHome, oldLifecycle })
			stop := func(context.Context, string) error { return nil }
			if code := runStopPeerGroup(config.DefaultOptions(), "review", stop); code != tc.want {
				t.Fatalf("exit %d, want %d", code, tc.want)
			}
		})
	}
}
