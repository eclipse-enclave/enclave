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
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/gateway"
	"enclave/internal/peergroup"
)

// fakeA2AEngine stands in for the engine and both hub readers: a reload
// applies the current snapshot unless the test withholds acknowledgments.
type fakeA2AEngine struct {
	t         *testing.T
	stateRoot string
	calls     []string
	hub       hubState
	noAck     bool
	epoch     int

	connectErr    error
	disconnectErr error
	sessions      map[string]memberSessionState
	sessionsMu    sync.Mutex
	signal        func(backend.SessionRef, a2a.CtlRequest) (backend.WorkerSignalResult, error)
}

func newFakeA2AEngine(t *testing.T, stateRoot string) *fakeA2AEngine {
	return &fakeA2AEngine{t: t, stateRoot: stateRoot, sessions: map[string]memberSessionState{}}
}

func (f *fakeA2AEngine) record(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeA2AEngine) StartHub(ctx context.Context, spec backend.HubSpec) (backend.HubRef, error) {
	if !f.hub.Exists && spec.Initial != nil {
		return f.createHub(spec, *spec.Initial)
	}
	f.record("StartHub")
	return f.hub.Ref, nil
}

func (f *fakeA2AEngine) StopHub(ctx context.Context, ref backend.HubRef) error {
	f.record("StopHub %s", ref.Name)
	if len(f.hub.Endpoints) > 0 {
		return errors.New("still attached")
	}
	f.hub = hubState{}
	return nil
}

func (f *fakeA2AEngine) ReloadHub(ctx context.Context, ref backend.HubRef) error {
	f.record("ReloadHub")
	if f.noAck {
		return nil
	}
	group := ref.Group
	data, err := os.ReadFile(filepath.Join(peergroup.CurrentLinkPath(f.stateRoot, group), peergroup.ManifestFile))
	if err != nil {
		f.t.Fatalf("reload without a published snapshot: %v", err)
	}
	var manifest peergroup.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		f.t.Fatal(err)
	}
	for _, reader := range []string{peergroup.ReaderSupervisor, peergroup.ReaderProxy} {
		name := peergroup.SupervisorStatus
		if reader == peergroup.ReaderProxy {
			name = peergroup.ProxyStatus
		}
		if err := peergroup.WriteStatus(filepath.Join(peergroup.StatusDir(f.stateRoot, group), name), peergroup.StatusFile{
			SchemaVersion: peergroup.SchemaVersion, Reader: reader, GroupGeneration: manifest.GroupGeneration,
			Epoch: fmt.Sprintf("%016x", f.epoch), AppliedRevision: manifest.Revision,
		}); err != nil {
			f.t.Fatal(err)
		}
	}
	return nil
}

func (f *fakeA2AEngine) ConnectHub(ctx context.Context, ref backend.HubRef, att backend.HubAttachment) error {
	f.record("ConnectHub %s %s", att.Network, att.IPv4)
	if f.connectErr != nil {
		return f.connectErr
	}
	f.hub.Endpoints[att.Network] = dockercmd.ContainerNetworkEndpoint{IPAddress: att.IPv4, NetworkID: att.Network}
	return nil
}

func (f *fakeA2AEngine) DisconnectHub(ctx context.Context, ref backend.HubRef, network string) error {
	f.record("DisconnectHub %s", network)
	if f.disconnectErr != nil {
		return f.disconnectErr
	}
	delete(f.hub.Endpoints, network)
	return nil
}

func (f *fakeA2AEngine) SignalWorker(ctx context.Context, ref backend.SessionRef, req a2a.CtlRequest) (backend.WorkerSignalResult, error) {
	f.record("SignalWorker %s %s", ref.Name, req.Op)
	if f.signal == nil {
		return backend.WorkerSignalResult{}, errors.New("unreachable")
	}
	return f.signal(ref, req)
}

func (f *fakeA2AEngine) ReloadGatewayOverlay(ctx context.Context, ref backend.SessionRef, generation uint64) error {
	f.record("ReloadGatewayOverlay %s %d", ref.Name, generation)
	return nil
}

func (f *fakeA2AEngine) ListHubs(ctx context.Context) ([]backend.HubInfo, error) {
	if !f.hub.Exists {
		return nil, nil
	}
	return []backend.HubInfo{{Ref: f.hub.Ref, GroupGeneration: f.hub.GroupGeneration, Running: f.hub.Running}}, nil
}

func (f *fakeA2AEngine) inspectHub(ctx context.Context, group string) (hubState, error) {
	return f.hub, nil
}

func (f *fakeA2AEngine) createHub(spec backend.HubSpec, att backend.HubAttachment) (backend.HubRef, error) {
	f.record("createHub %s %s", att.Network, att.IPv4)
	f.epoch++
	ref := backend.HubRef{Group: spec.Group, Name: HubContainerName(spec.Group)}
	f.hub = hubState{
		Exists: true, Running: true, Ref: ref, GroupGeneration: spec.GroupGeneration,
		Endpoints: map[string]dockercmd.ContainerNetworkEndpoint{att.Network: {IPAddress: att.IPv4, NetworkID: att.Network}},
	}
	return ref, nil
}

func (f *fakeA2AEngine) restartHub(ctx context.Context, spec backend.HubSpec, state hubState) error {
	f.record("restartHub")
	f.epoch++
	f.hub.Running = true
	return nil
}

func (f *fakeA2AEngine) removeHub(ctx context.Context, ref backend.HubRef) error {
	f.record("removeHub")
	f.hub = hubState{}
	return nil
}

func (f *fakeA2AEngine) usedAddresses(ctx context.Context, network, exclude string) (map[netip.Addr]string, error) {
	return map[netip.Addr]string{}, nil
}

func (f *fakeA2AEngine) memberSession(ctx context.Context, container string) (memberSessionState, error) {
	f.sessionsMu.Lock()
	defer f.sessionsMu.Unlock()
	return f.sessions[container], nil
}

// setSession guards the sessions map against the lock hold of Join.Started.
func (f *fakeA2AEngine) setSession(container string, state memberSessionState) {
	f.sessionsMu.Lock()
	defer f.sessionsMu.Unlock()
	f.sessions[container] = state
}

func (f *fakeA2AEngine) networkExists(ctx context.Context, network string) (bool, error) {
	return true, nil
}

type a2aFixture struct {
	t      *testing.T
	home   string
	state  string
	engine *fakeA2AEngine
	p      *PeerGroups
}

func newA2AFixture(t *testing.T) *a2aFixture {
	t.Helper()
	home := t.TempDir()
	state := filepath.Join(home, "state")
	engine := newFakeA2AEngine(t, state)
	p := newPeerGroups(engine, home, state, PeerGroupConfig{CommitTimeout: 2 * time.Second, DepartureAckTimeout: time.Second}, peergroup.NewSnapshotPublisher(state, nil))
	p.engineID = peergroup.Engine{Name: "docker", Root: "/var/lib/docker"}
	return &a2aFixture{t: t, home: home, state: state, engine: engine, p: p}
}

func (f *a2aFixture) spec(member string, role a2a.Role) JoinSpec {
	return JoinSpec{
		Group: "review", Role: role, Member: member,
		SessionContainer: "enclave-claude-ab12cd34ef56-" + member,
		Tool:             "claude", ProjectHash: "ab12cd34ef56", ConfigStoreKey: "default",
		ConfigStoreDir:     filepath.Join(f.home, "store", member),
		ContainerConfigDir: "/home/agent/.claude",
		ProjectPath:        filepath.Join(f.home, "src", member),
		MountMode:          peergroup.MountReadWrite,
		Description:        member + " does things",
		Skills:             []a2a.AgentSkill{{ID: member + "-skill", Name: member}},
	}
}

// join runs the whole join as a session start would: prepare, network,
// gateway, then the container is "started".
func (f *a2aFixture) join(member string, role a2a.Role, subnet string) *Join {
	f.t.Helper()
	j, err := f.p.Join(context.Background(), f.spec(member, role))
	if err != nil {
		f.t.Fatalf("Join(%s) error = %v", member, err)
	}
	ctx := context.Background()
	network := "net-" + member
	if err := j.beforeGateway(ctx, network, subnet); err != nil {
		f.t.Fatalf("beforeGateway(%s) error = %v", member, err)
	}
	gw := netip.MustParsePrefix(subnet).Addr().Next().Next().String()
	if err := j.afterGateway(ctx, gw); err != nil {
		f.t.Fatalf("afterGateway(%s) error = %v", member, err)
	}
	f.engine.sessions[j.spec.SessionContainer] = memberSessionState{Exists: true, Running: true, Labels: map[string]string{peergroup.LabelMemberGeneration: string(j.generation)}}
	f.engine.sessions[gateway.ContainerName(j.spec.SessionContainer)] = memberSessionState{Exists: true, Running: true}
	j.Started()
	return j
}

func (f *a2aFixture) record() *peergroup.GroupRecord {
	f.t.Helper()
	rec, err := f.p.store.Load("review")
	if err != nil {
		f.t.Fatalf("load group record: %v", err)
	}
	return rec
}

func (f *a2aFixture) assertUnlocked() {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	unlock, err := f.p.store.Lock(ctx, "review")
	if err != nil {
		f.t.Fatalf("group lock still held: %v", err)
	}
	unlock()
}

func TestJoinCreatesHubOnFirstMemberBridgeAndCommits(t *testing.T) {
	f := newA2AFixture(t)
	j := f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	f.assertUnlocked()

	rec := f.record()
	if len(rec.Members) != 1 || rec.Members[0].State != peergroup.MemberActive || rec.Committed != 1 || rec.Pending != nil {
		t.Fatalf("record after join = %+v", rec)
	}
	m := rec.Members[0]
	if m.HubAddress != "172.18.0.14" || m.GatewayAddress != "172.18.0.2" || m.Network != "net-orch" || m.AdapterPort != 0 {
		t.Fatalf("member network facts = %+v", m)
	}
	if got := strings.Join(f.engine.calls, ","); got != "createHub net-orch 172.18.0.14,ReloadHub" {
		t.Fatalf("engine calls = %s", got)
	}
	inv, err := f.p.inventory.Load("review", rec.GroupGeneration)
	if err != nil || len(inv.Entries) != 1 || inv.Entries[0].State != peergroup.InventoryMember {
		t.Fatalf("inventory = %+v, %v", inv, err)
	}
	var overlay peergroup.Overlay
	data, err := os.ReadFile(j.overlayPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &overlay); err != nil {
		t.Fatal(err)
	}
	if !overlay.Active || overlay.Generation != 1 || overlay.HubAddress != "172.18.0.14" || overlay.DNSName != a2a.HubDNSName || overlay.AdapterPort != 0 {
		t.Fatalf("overlay = %+v", overlay)
	}
	session := j.Session()
	if session.OverlayDir != filepath.Dir(j.overlayPath) || session.Labels[peergroup.LabelRole] != "orchestrator" || session.Labels[peergroup.LabelMemberGeneration] != string(j.generation) {
		t.Fatalf("session = %+v", session)
	}
	env := map[string]string{}
	for _, e := range session.Env {
		env[e.Name] = e.Value
	}
	if env[a2a.EnvInboundToken] == "" || env[a2a.EnvOutboundToken] != "" || env[a2a.EnvHubHost] != a2a.HubDNSName {
		t.Fatalf("orchestrator env = %v", env)
	}
	if got, err := joinFor(session); err != nil || got != j {
		t.Fatalf("session does not carry its join: %v, %v", got, err)
	}
	if err := j.OnSessionNetwork(context.Background(), "net-other"); !errors.Is(err, errJoinFinished) {
		t.Fatalf("a started join accepted another start: %v", err)
	}
}

func TestJoinConnectsExistingHubAndPublishesWorker(t *testing.T) {
	f := newA2AFixture(t)
	f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	f.engine.calls = nil
	j := f.join("worker-1", a2a.RoleWorker, "172.18.0.16/28")

	if got := strings.Join(f.engine.calls, ","); got != "ConnectHub net-worker-1 172.18.0.30,ReloadHub" {
		t.Fatalf("engine calls = %s", got)
	}
	rec := f.record()
	if rec.Committed != 2 || len(rec.Members) != 2 {
		t.Fatalf("record = %+v", rec)
	}
	m, err := readCurrentMembership(f.state)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Members) != 2 || m.Members[1].Skills[0].ID != "worker-1-skill" || m.Members[0].Skills[0].ID != "orch-skill" {
		t.Fatalf("membership keeps skills across revisions: %+v", m.Members)
	}
	env := map[string]string{}
	for _, e := range j.Session().Env {
		env[e.Name] = e.Value
	}
	if env[a2a.EnvOutboundToken] == "" || env[a2a.EnvAdapterPort] != "41821" || env[a2a.EnvTableDir] != "/home/agent/.claude/a2a/"+string(j.generation) {
		t.Fatalf("worker env = %v", env)
	}
	stored, err := f.p.tokens.LoadOutbound("review")
	if err != nil || len(stored.Outbound) != 1 || stored.Outbound[0].Member != "worker-1" {
		t.Fatalf("host copy of outbound tokens = %+v, %v", stored, err)
	}
}

func readCurrentMembership(state string) (peergroup.Membership, error) {
	var m peergroup.Membership
	data, err := os.ReadFile(filepath.Join(peergroup.CurrentLinkPath(state, "review"), peergroup.MembershipFile))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(data, &m)
}

func TestJoinRollsBackEveryStepWhenCommitTimesOut(t *testing.T) {
	f := newA2AFixture(t)
	f.p.cfg.CommitTimeout = 200 * time.Millisecond
	j, err := f.p.Join(context.Background(), f.spec("orch", a2a.RoleOrchestrator))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := j.beforeGateway(ctx, "net-orch", "172.18.0.0/28"); err != nil {
		t.Fatal(err)
	}
	f.engine.noAck = true
	err = j.afterGateway(ctx, "172.18.0.2")
	if !errors.Is(err, peergroup.ErrNotCommitted) {
		t.Fatalf("afterGateway error = %v, want ErrNotCommitted", err)
	}
	f.engine.noAck = false
	if err := j.Fail(ctx); err != nil {
		t.Fatalf("rollback error = %v", err)
	}
	f.assertUnlocked()

	wantTrace := []string{
		"load or create group record", "check member and worktree guard", "write cleanup inventory entry",
		"issue member tokens", "record joining member", "assign hub address", "write gateway overlay",
		"attach hub", "publish snapshot with member", "await snapshot acknowledgments",
		"undo publish snapshot with member", "undo attach hub", "undo write gateway overlay",
		"undo record joining member", "undo issue member tokens", "undo write cleanup inventory entry",
		"undo load or create group record",
	}
	if got := strings.Join(j.steps.trace, "|"); got != strings.Join(wantTrace, "|") {
		t.Fatalf("trace =\n%s\nwant\n%s", got, strings.Join(wantTrace, "|"))
	}
	if _, err := f.p.store.Load("review"); !errors.Is(err, peergroup.ErrGroupNotFound) {
		t.Fatalf("group created by the failed join survives: %v", err)
	}
	if f.engine.hub.Exists {
		t.Fatal("hub created by the failed join survives")
	}
	if _, err := os.Stat(filepath.Dir(j.overlayPath)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("overlay dir survives: %v", err)
	}
	if gens, _ := f.p.inventory.Generations("review"); len(gens) != 0 {
		t.Fatalf("inventory survives: %v", gens)
	}
}

func TestJoinRollbackOfSecondMemberPublishesRemovalAndDisconnects(t *testing.T) {
	f := newA2AFixture(t)
	f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	f.p.cfg.CommitTimeout = 200 * time.Millisecond
	j, err := f.p.Join(context.Background(), f.spec("worker-1", a2a.RoleWorker))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := j.beforeGateway(ctx, "net-worker-1", "172.18.0.16/28"); err != nil {
		t.Fatal(err)
	}
	if err := j.afterGateway(ctx, "172.18.0.18"); err != nil {
		t.Fatal(err)
	}
	// The container start failed after commit: the whole join is undone.
	f.engine.calls = nil
	if err := j.Fail(ctx); err != nil {
		t.Fatalf("rollback error = %v", err)
	}
	if got := strings.Join(f.engine.calls, ","); got != "ReloadHub,DisconnectHub net-worker-1" {
		t.Fatalf("rollback engine calls = %s", got)
	}
	rec := f.record()
	if len(rec.Members) != 1 || rec.Committed != 3 || rec.Pending != nil {
		t.Fatalf("record after rollback = %+v", rec)
	}
	m, err := readCurrentMembership(f.state)
	if err != nil || len(m.Members) != 1 || m.Members[0].Name != "orch" {
		t.Fatalf("current membership after rollback = %+v, %v", m, err)
	}
	stored, err := f.p.tokens.LoadOutbound("review")
	if err != nil || len(stored.Outbound) != 0 {
		t.Fatalf("outbound token survives rollback: %+v, %v", stored, err)
	}
	f.assertUnlocked()
}

func TestJoinConnectFailureRollsBackHostSteps(t *testing.T) {
	f := newA2AFixture(t)
	f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	f.engine.connectErr = errors.Join(errors.New("Address already in use"), dockercmd.ErrStaleEndpoint)
	j, err := f.p.Join(context.Background(), f.spec("worker-1", a2a.RoleWorker))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := j.beforeGateway(ctx, "net-worker-1", "172.18.0.16/28"); err != nil {
		t.Fatal(err)
	}
	if err := j.afterGateway(ctx, "172.18.0.18"); !errors.Is(err, dockercmd.ErrStaleEndpoint) {
		t.Fatalf("afterGateway error = %v", err)
	}
	_ = j.Fail(ctx)
	rec := f.record()
	if len(rec.Members) != 1 || rec.Committed != 1 {
		t.Fatalf("record after failed connect = %+v", rec)
	}
	if hasCallWithPrefix(f.engine.calls, "DisconnectHub") {
		t.Fatal("rollback disconnected an attachment the failed connect never made; ConnectHub verifies its own cleanup")
	}
}

func TestJoinRejectsDuplicatesAndWorktreeClash(t *testing.T) {
	f := newA2AFixture(t)
	f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	for name, spec := range map[string]JoinSpec{
		"duplicate name":      f.spec("orch", a2a.RoleWorker),
		"second orchestrator": f.spec("orch-2", a2a.RoleOrchestrator),
		"worktree clash": func() JoinSpec {
			s := f.spec("worker-1", a2a.RoleWorker)
			s.ProjectPath = filepath.Join(f.home, "src", "orch")
			return s
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.p.Join(context.Background(), spec); err == nil {
				t.Fatal("Join() succeeded")
			}
			f.assertUnlocked()
			if rec := f.record(); len(rec.Members) != 1 {
				t.Fatalf("members after refused join = %+v", rec.Members)
			}
		})
	}
}

func TestJoinAbortBeforeSessionStart(t *testing.T) {
	f := newA2AFixture(t)
	j, err := f.p.Join(context.Background(), f.spec("orch", a2a.RoleOrchestrator))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.assertUnlocked()
	if err := j.OnSessionNetwork(context.Background(), "net-orch"); !errors.Is(err, errJoinFinished) {
		t.Fatalf("aborted join accepted a session start: %v", err)
	}
	if _, err := f.p.store.Load("review"); !errors.Is(err, peergroup.ErrGroupNotFound) {
		t.Fatalf("group survives abort: %v", err)
	}
}

func TestLeaveWorkerRevokesDisconnectsAndClearsOverlay(t *testing.T) {
	f := newA2AFixture(t)
	f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	w := f.join("worker-1", a2a.RoleWorker, "172.18.0.16/28")
	f.engine.calls = nil

	res, err := f.p.Leave(context.Background(), LeaveSpec{Group: "review", Member: "worker-1"})
	if err != nil || res.Degraded || !res.Left || res.GroupRemoved {
		t.Fatalf("Leave() = %+v, %v", res, err)
	}
	want := "ReloadHub,DisconnectHub net-worker-1,ReloadGatewayOverlay enclave-claude-ab12cd34ef56-worker-1 2"
	if got := strings.Join(f.engine.calls, ","); got != want {
		t.Fatalf("engine calls = %s, want %s", got, want)
	}
	rec := f.record()
	if len(rec.Members) != 1 || rec.Committed != 3 {
		t.Fatalf("record after leave = %+v", rec)
	}
	if _, err := os.Stat(filepath.Dir(w.overlayPath)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("overlay dir survives leave: %v", err)
	}
	inv, _ := f.p.inventory.Load("review", rec.GroupGeneration)
	for _, e := range inv.Entries {
		if e.Member == "worker-1" && e.State != peergroup.InventoryEligible {
			t.Fatalf("worker inventory state = %s", e.State)
		}
	}
}

func TestLeaveOrchestratorCollectsDepartureAcks(t *testing.T) {
	f := newA2AFixture(t)
	f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	w1 := f.join("worker-1", a2a.RoleWorker, "172.18.0.16/28")
	w2 := f.join("worker-2", a2a.RoleWorker, "172.18.0.32/28")
	f.engine.signal = func(ref backend.SessionRef, req a2a.CtlRequest) (backend.WorkerSignalResult, error) {
		if req.Op != a2a.CtlDepart || !req.Authoritative || req.Principal.Member != "orch" {
			t.Fatalf("ctl request = %+v", req)
		}
		termination := a2a.TerminationConfirmed
		worker, gen := w1.spec.Member, w1.generation
		if ref.Name == w2.spec.SessionContainer {
			termination, worker, gen = a2a.TerminationUnconfirmed, w2.spec.Member, w2.generation
		}
		return backend.WorkerSignalResult{Response: a2a.CtlResponse{Op: req.Op, OK: true, Departure: &a2a.DepartureAck{
			Worker: worker, WorkerGeneration: string(gen), Principal: req.Principal, AdmissionStopPersisted: true, Termination: termination,
		}}}, nil
	}
	res, err := f.p.Leave(context.Background(), LeaveSpec{Group: "review", Member: "orch"})
	if err != nil || !res.Degraded || res.Departure == nil || res.Departure.State != peergroup.DepartureDegraded {
		t.Fatalf("Leave() = %+v, %v", res, err)
	}
	rec := f.record()
	kinds := map[string]string{}
	for _, ob := range rec.Obligations {
		kinds[ob.Kind] = ob.Member
	}
	if kinds[peergroup.ObligationDepartureAck] != "orch" || kinds[peergroup.ObligationQuarantineRelease] != "worker-2" {
		t.Fatalf("obligations = %+v", rec.Obligations)
	}

	// Reconcile retries: worker-2 now confirms and its quarantine is released.
	f.engine.signal = func(ref backend.SessionRef, req a2a.CtlRequest) (backend.WorkerSignalResult, error) {
		switch req.Op {
		case a2a.CtlDepart:
			return backend.WorkerSignalResult{Response: a2a.CtlResponse{Op: req.Op, OK: true, Departure: &a2a.DepartureAck{
				Worker: w2.spec.Member, WorkerGeneration: string(w2.generation), Termination: a2a.TerminationConfirmed,
			}}}, nil
		case a2a.CtlReleaseQuarantine:
			alive := false
			return backend.WorkerSignalResult{Response: a2a.CtlResponse{Op: req.Op, OK: true, DescendantsAlive: &alive}}, nil
		}
		return backend.WorkerSignalResult{}, fmt.Errorf("unexpected op %s", req.Op)
	}
	rres, err := f.p.Reconcile(context.Background(), "review")
	if err != nil || rres.Degraded {
		t.Fatalf("Reconcile() = %+v, %v", rres, err)
	}
	rec = f.record()
	if len(rec.Obligations) != 0 || rec.Departures[0].State != peergroup.DepartureComplete || len(rec.Releases) != 1 {
		t.Fatalf("record after reconcile = %+v", rec)
	}
}

func TestLeaveDegradesWhenRemovalDoesNotCommit(t *testing.T) {
	f := newA2AFixture(t)
	f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	f.join("worker-1", a2a.RoleWorker, "172.18.0.16/28")
	f.p.cfg.CommitTimeout = 200 * time.Millisecond
	f.engine.noAck = true
	f.engine.calls = nil
	res, err := f.p.Leave(context.Background(), LeaveSpec{Group: "review", Member: "worker-1"})
	if err != nil || !res.Degraded {
		t.Fatalf("Leave() = %+v, %v", res, err)
	}
	if !hasCallWithPrefix(f.engine.calls, "DisconnectHub net-worker-1") {
		t.Fatalf("connectivity fallback missing: %v", f.engine.calls)
	}
	rec := f.record()
	if !hasObligation(rec, peergroup.ObligationSnapshotCommit) || rec.Pending == nil {
		t.Fatalf("record after degraded leave = %+v", rec)
	}
	// Reconcile replaces the uncommitted revision once the hub acknowledges.
	f.engine.noAck = false
	rres, err := f.p.Reconcile(context.Background(), "review")
	if err != nil || rres.Degraded {
		t.Fatalf("Reconcile() = %+v, %v", rres, err)
	}
	rec = f.record()
	if hasObligation(rec, peergroup.ObligationSnapshotCommit) || rec.Pending != nil {
		t.Fatalf("record after reconcile = %+v", rec)
	}
}

func TestLeaveOfLastMemberTearsDownGroupAndDeletesStores(t *testing.T) {
	f := newA2AFixture(t)
	j := f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	table := peergroup.ContextTableDir(j.spec.ConfigStoreDir, j.generation)
	transcript := filepath.Join(j.spec.ConfigStoreDir, "projects", "enc", "sid.jsonl")
	for _, dir := range []string{table, filepath.Dir(transcript)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	contexts, _ := json.Marshal(a2aagent.ContextsFileData{Contexts: []a2aagent.ContextRecord{{ContextID: "c1", Transcripts: []string{"projects/enc/sid.jsonl"}}}})
	if err := os.WriteFile(filepath.Join(table, a2aagent.ContextsFile), contexts, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := f.p.Leave(context.Background(), LeaveSpec{Group: "review", Member: "orch"})
	if err != nil || !res.GroupRemoved || !res.HubRemoved {
		t.Fatalf("Leave() = %+v, %v", res, err)
	}
	for _, path := range []string{table, transcript} {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s survives teardown: %v", path, err)
		}
	}
	if _, err := f.p.store.Load("review"); !errors.Is(err, peergroup.ErrGroupNotFound) {
		t.Fatalf("group record survives teardown: %v", err)
	}
	if _, err := os.Stat(peergroup.GroupDir(f.state, "review")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("group directory survives teardown: %v", err)
	}
}

func TestReconcileRemovesGoneMembersAndCrashedJoins(t *testing.T) {
	f := newA2AFixture(t)
	f.join("orch", a2a.RoleOrchestrator, "172.18.0.0/28")
	w := f.join("worker-1", a2a.RoleWorker, "172.18.0.16/28")
	// A crashed join: prepared and attached, then the process died.
	crashed, err := f.p.Join(context.Background(), f.spec("worker-2", a2a.RoleWorker))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := crashed.beforeGateway(ctx, "net-worker-2", "172.18.0.32/28"); err != nil {
		t.Fatal(err)
	}
	f.engine.hub.Endpoints["net-worker-2"] = dockercmd.ContainerNetworkEndpoint{IPAddress: "172.18.0.46"}
	if err := f.p.store.Save(crashed.rec); err != nil {
		t.Fatal(err)
	}
	crashed.unlock()
	// worker-1's session container is gone.
	delete(f.engine.sessions, w.spec.SessionContainer)
	delete(f.engine.sessions, gateway.ContainerName(w.spec.SessionContainer))
	// A stray attachment no member owns.
	f.engine.hub.Endpoints["net-stray"] = dockercmd.ContainerNetworkEndpoint{IPAddress: "10.0.0.5"}

	res, err := f.p.Reconcile(ctx, "review")
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	rec := f.record()
	if len(rec.Members) != 1 || rec.Members[0].Name != "orch" {
		t.Fatalf("members after reconcile = %+v (actions %v)", rec.Members, res.Actions)
	}
	for _, network := range []string{"net-worker-1", "net-worker-2", "net-stray"} {
		if _, ok := f.engine.hub.Endpoints[network]; ok {
			t.Fatalf("hub still attached to %s (calls %v)", network, f.engine.calls)
		}
	}
	m, err := readCurrentMembership(f.state)
	if err != nil || len(m.Members) != 1 {
		t.Fatalf("membership after reconcile = %+v, %v", m, err)
	}
}

func TestReconcileReportsBusyGroup(t *testing.T) {
	f := newA2AFixture(t)
	previous := reconcileLockWait
	reconcileLockWait = 100 * time.Millisecond
	t.Cleanup(func() { reconcileLockWait = previous })
	j, err := f.p.Join(context.Background(), f.spec("orch", a2a.RoleOrchestrator))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Abort(context.Background()) }()
	res, err := f.p.Reconcile(context.Background(), "review")
	if err != nil || !res.Busy {
		t.Fatalf("Reconcile() = %+v, %v", res, err)
	}
}

func TestReconcileWithoutRecordRemovesOrphanHub(t *testing.T) {
	f := newA2AFixture(t)
	f.engine.hub = hubState{Exists: true, Ref: backend.HubRef{Group: "review", Name: HubContainerName("review")}, Endpoints: map[string]dockercmd.ContainerNetworkEndpoint{}}
	results, err := f.p.ReconcileAll(context.Background())
	if err != nil || len(results) != 1 || f.engine.hub.Exists {
		t.Fatalf("ReconcileAll() = %+v, %v; hub %+v", results, err, f.engine.hub)
	}
}

func TestA2AStepsRollbackContinuesPastFailedUndo(t *testing.T) {
	var steps a2aSteps
	var undone []string
	step := func(name string, undoErr error) a2aStep {
		return a2aStep{name: name, do: func(context.Context) error { return nil }, undo: func(context.Context) error {
			undone = append(undone, name)
			return undoErr
		}}
	}
	if err := steps.run(context.Background(), step("a", nil), step("b", errors.New("boom")), step("c", nil)); err != nil {
		t.Fatal(err)
	}
	failing := a2aStep{name: "d", do: func(context.Context) error { return errors.New("fail") }}
	if err := steps.run(context.Background(), failing); err == nil || !strings.Contains(err.Error(), "d: fail") {
		t.Fatalf("run error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := steps.rollback(ctx)
	if err == nil || !strings.Contains(err.Error(), "undo b: boom") {
		t.Fatalf("rollback error = %v", err)
	}
	if strings.Join(undone, ",") != "c,b,a" {
		t.Fatalf("undo order = %v", undone)
	}
}
