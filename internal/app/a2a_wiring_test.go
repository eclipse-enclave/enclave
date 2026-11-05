// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"enclave/internal/a2a"
	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/peergroup"
	"enclave/internal/runtime"
)

func peerGroupOptions() model.Options {
	opts := config.DefaultOptions()
	opts.Backend = backend.NameDocker
	opts.ImageName = "enclave-claude:latest"
	opts.PeerGroup = "review"
	opts.SessionName = "worker-1"
	return opts
}

func TestValidateA2ARunRejections(t *testing.T) {
	cases := map[string]struct {
		mutate func(*model.Options, *model.OptionSources)
		want   string
	}{
		"role without group": {func(o *model.Options, s *model.OptionSources) {
			o.PeerGroup = ""
			o.A2ARole = "orchestrator"
			s.A2ARole = model.SourceCLI
		}, "--role requires --peer-group"},
		"description without group": {func(o *model.Options, s *model.OptionSources) {
			o.PeerGroup = ""
			s.A2ADescription = model.SourceCLI
		}, "--a2a-description requires --peer-group"},
		"qemu": {func(o *model.Options, _ *model.OptionSources) { o.Backend = backend.NameQEMU },
			"--peer-group requires --backend docker or podman"},
		"unrestricted network": {func(o *model.Options, _ *model.OptionSources) { o.AllowAllNetwork = true },
			"--peer-group is incompatible with --allow-all-network"},
		"no name": {func(o *model.Options, _ *model.OptionSources) { o.SessionName = "" },
			"--peer-group requires --name"},
		"bad bound": {func(o *model.Options, _ *model.OptionSources) { o.A2AQueueDepth = "many" },
			"invalid A2A options: a2a_queue_depth"},
		"bad group": {func(o *model.Options, _ *model.OptionSources) { o.PeerGroup = "Review!" },
			"invalid A2A options: peer_group"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			opts := peerGroupOptions()
			sources := model.DefaultOptionSources()
			tc.mutate(&opts, &sources)
			_, _, _, err := ValidateOptions(opts, sources, ValidationContext{Action: "run"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestValidateA2ARunAccepts(t *testing.T) {
	if _, _, _, err := ValidateOptions(peerGroupOptions(), model.DefaultOptionSources(), ValidationContext{Action: "run"}); err != nil {
		t.Fatalf("peer group session: %v", err)
	}
	// Configured A2A values without a group wait for a session that joins one.
	plain := peerGroupOptions()
	plain.PeerGroup = ""
	plain.A2ARole = "orchestrator"
	sources := model.DefaultOptionSources()
	sources.A2ARole = model.SourceProject
	if _, _, _, err := ValidateOptions(plain, sources, ValidationContext{Action: "run"}); err != nil {
		t.Fatalf("configured role without a group: %v", err)
	}
	// Other actions never join, so the run-path checks do not apply.
	stray := peerGroupOptions()
	stray.SessionName = ""
	if _, _, _, err := ValidateOptions(stray, model.DefaultOptionSources(), ValidationContext{Action: "exec"}); err != nil {
		t.Fatalf("exec with a configured group: %v", err)
	}
}

func claudeA2AProfile() model.Profile {
	return model.Profile{
		Name:     "claude",
		YoloFlag: "--dangerously-skip-permissions",
		A2A: &model.A2ASpec{
			Command:          []string{"claude", "-p"},
			ResumeArgs:       []string{"--resume", model.A2ASessionIDPlaceholder},
			EventFormat:      model.A2AEventFormatClaudeStreamJSON,
			Skills:           []model.A2ASkillSpec{{ID: "coding", Name: "Coding", Description: "codes"}},
			OrchestratorArgs: []string{"--mcp-config", "/x.json"},
		},
	}
}

func TestValidateA2AProfile(t *testing.T) {
	worker := peerGroupOptions()
	noA2A := claudeA2AProfile()
	noA2A.A2A = nil
	noYoloFlag := claudeA2AProfile()
	noYoloFlag.YoloFlag = ""
	shell := peerGroupOptions()
	shell.Shell = true
	cases := map[string]struct {
		opts    model.Options
		profile model.Profile
		yolo    bool
		want    string
	}{
		"worker without a2a block": {worker, noA2A, true, "has no sandbox.a2a block"},
		"worker without yolo":      {worker, claudeA2AProfile(), false, "--role worker needs yolo mode, but it is disabled"},
		"tool without yolo flag":   {worker, noYoloFlag, true, "has no yolo flag"},
		"worker shell":             {shell, claudeA2AProfile(), true, "cannot start a shell"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateA2AProfile(tc.opts, tc.profile, tc.yolo)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	orchestrator := peerGroupOptions()
	orchestrator.A2ARole = "orchestrator"
	if err := validateA2AProfile(orchestrator, noA2A, false); err != nil {
		t.Fatalf("orchestrator without yolo or a2a block: %v", err)
	}
	plain := peerGroupOptions()
	plain.PeerGroup = ""
	if err := validateA2AProfile(plain, noA2A, false); err != nil {
		t.Fatalf("plain session: %v", err)
	}
}

func envMap(env []backend.EnvVar) map[string]string {
	m := map[string]string{}
	for _, e := range env {
		m[e.Name] = e.Value
	}
	return m
}

func TestA2ASessionEnv(t *testing.T) {
	opts := peerGroupOptions()
	worker, err := a2aSessionEnv(opts, a2a.RoleWorker, claudeA2AProfile())
	if err != nil {
		t.Fatal(err)
	}
	got := envMap(worker)
	want := map[string]string{
		a2a.EnvMaxRequestBytes:    "1048576",
		a2a.EnvReconnectWindow:    "1m0s",
		a2a.EnvQueueDepth:         "8",
		a2a.EnvTaskDeadline:       "30m0s",
		a2a.EnvMaxResultBytes:     "262144",
		a2a.EnvRecordLimit:        "1000",
		a2a.EnvRecordMaxAge:       "168h0m0s",
		a2a.EnvContextTTL:         "24h0m0s",
		a2a.EnvTerminationTimeout: "10s",
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s = %q, want %q", name, got[name], value)
		}
	}
	if _, ok := got[a2a.EnvSendWait]; ok {
		t.Errorf("worker env carries %s", a2a.EnvSendWait)
	}
	// Decoded the way the adapter does: the block plus yoloFlag, nothing else.
	var harness a2aHarnessEnv
	dec := json.NewDecoder(strings.NewReader(got[a2a.EnvHarness]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&harness); err != nil {
		t.Fatalf("decode %s: %v", a2a.EnvHarness, err)
	}
	if harness.YoloFlag != "--dangerously-skip-permissions" || harness.EventFormat != model.A2AEventFormatClaudeStreamJSON {
		t.Fatalf("harness = %+v", harness)
	}
	for name, value := range got {
		if strings.Contains(value, "\n") {
			t.Errorf("%s has a newline, which the env file refuses", name)
		}
	}

	orch, err := a2aSessionEnv(opts, a2a.RoleOrchestrator, model.Profile{Name: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	gotOrch := envMap(orch)
	if gotOrch[a2a.EnvSendWait] != "1m30s" || gotOrch[a2a.EnvHarness] != "" || gotOrch[a2a.EnvQueueDepth] != "" {
		t.Fatalf("orchestrator env = %v", gotOrch)
	}

	hub, err := a2aHubEnv(opts)
	if err != nil {
		t.Fatal(err)
	}
	if h := envMap(hub); h[a2a.EnvHubLogMode] != "coarse" || h[a2a.EnvHubRetainedResults] != "100" || h[a2a.EnvMaxRequestBytes] != "1048576" || h[a2a.EnvReconnectWindow] != "1m0s" {
		t.Fatalf("hub env = %v", h)
	}
}

type fakeA2AJoin struct {
	session *backend.PeerGroupSession
	leave   *backenddocker.LeaveResult
}

func (j *fakeA2AJoin) Session() *backend.PeerGroupSession      { return j.session }
func (j *fakeA2AJoin) Warnings() []string                      { return []string{"worktree shared with lead"} }
func (j *fakeA2AJoin) Generation() peergroup.Generation        { return "a1b2c3d4e5f60718" }
func (j *fakeA2AJoin) LeaveResult() *backenddocker.LeaveResult { return j.leave }

type fakeA2AJoiner struct {
	calls []string
	spec  backenddocker.JoinSpec
	join  *fakeA2AJoin
	err   error
}

func (f *fakeA2AJoiner) ReconcileAll(context.Context) ([]backenddocker.ReconcileResult, error) {
	f.calls = append(f.calls, "reconcile-all")
	return nil, nil
}

func (f *fakeA2AJoiner) Join(_ context.Context, spec backenddocker.JoinSpec) (a2aJoin, error) {
	f.calls = append(f.calls, "join")
	f.spec = spec
	if f.err != nil {
		return nil, f.err
	}
	return f.join, nil
}

func TestA2ARunJoinsAfterReconcile(t *testing.T) {
	home := t.TempDir()
	joiner := &fakeA2AJoiner{join: &fakeA2AJoin{session: &backend.PeerGroupSession{Group: "review"}}}
	run := &a2aRun{
		group: "review", role: a2a.RoleOrchestrator, home: home, tool: "claude",
		project: model.Project{Hash: "abc123abc123", RealDir: "/src/app"},
		desc:    "leads", peers: joiner,
	}
	session, err := run.joinSession(context.Background(), runtime.PeerGroupMember{
		SessionContainer:   "enclave-claude-abc123abc123-lead",
		Member:             "lead",
		ConfigStore:        backend.StoreKey{Owner: "claude", ProjectHash: "abc123abc123", Suffix: "lead"},
		ContainerConfigDir: "/home/agent/.claude",
		ProjectReadOnly:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(joiner.calls, []string{"reconcile-all", "join"}) {
		t.Fatalf("calls = %v", joiner.calls)
	}
	spec := joiner.spec
	wantStore := config.HostStoreConfigDir(home, "claude", "abc123abc123", "lead")
	if spec.ConfigStoreDir != wantStore || spec.ConfigStoreKey != "lead" || spec.MountMode != peergroup.MountReadOnly ||
		spec.Role != a2a.RoleOrchestrator || spec.Member != "lead" || spec.ProjectPath != "/src/app" || spec.Description != "leads" {
		t.Fatalf("join spec = %+v", spec)
	}
	if got := envMap(session.Env)[a2a.EnvTableDir]; got != "/home/agent/.claude/a2a/a1b2c3d4e5f60718" {
		t.Fatalf("orchestrator %s = %q", a2a.EnvTableDir, got)
	}

	joiner.err = errors.New("member name lead is taken")
	if _, err := run.joinSession(context.Background(), runtime.PeerGroupMember{ConfigStore: backend.StoreKey{Owner: "claude", ProjectHash: "abc123abc123"}}); err == nil {
		t.Fatal("a failed join must fail the start")
	}
}

func TestA2ARunExitCode(t *testing.T) {
	var none *a2aRun
	if none.exitCode(5) != 5 {
		t.Fatal("plain session exit code changed")
	}
	join := &fakeA2AJoin{}
	run := &a2aRun{group: "review", join: join}
	if run.exitCode(0) != 0 {
		t.Fatal("no leave yet")
	}
	join.leave = &backenddocker.LeaveResult{Left: true, Degraded: true}
	if run.exitCode(0) != a2aExitDegraded {
		t.Fatal("degraded leave must exit 3")
	}
	if run.exitCode(2) != 2 {
		t.Fatal("a tool failure keeps its exit code")
	}
}

func TestStopPeerGroupRequestedOnlyFromCLI(t *testing.T) {
	opts := config.DefaultOptions()
	opts.PeerGroup = "review"
	opts.Sources.PeerGroup = model.SourceProject
	if _, ok := stopPeerGroupRequested(opts); ok {
		t.Fatal("a configured peer_group turned stop into a group stop")
	}
	opts.Sources.PeerGroup = model.SourceCLI
	if group, ok := stopPeerGroupRequested(opts); !ok || group != "review" {
		t.Fatalf("group stop = %q, %v", group, ok)
	}
	opts.CmdArgs = []string{"worker-1"}
	if err := validateStopPeerGroup(opts); err == nil {
		t.Fatal("stop --peer-group with a session argument must be refused")
	}
	opts.CmdArgs = nil
	opts.SessionName = "worker-1"
	opts.Sources.SessionName = model.SourceCLI
	if err := validateStopPeerGroup(opts); err == nil {
		t.Fatal("stop --peer-group with --name must be refused")
	}
}

func TestStopMembersLeaveBeforeStopOrchestratorFirst(t *testing.T) {
	f := newA2AFixture(t)
	lc := &reconcilingLifecycle{fakeLifecycle: &fakeLifecycle{degraded: map[string]bool{"lead": true}}, open: true}
	be := &stopTestBackend{}
	prev := a2aStopLeaver
	t.Cleanup(func() { a2aStopLeaver = prev })
	a2aStopLeaver = func(context.Context, backend.Backend, model.Options, []string) *a2aLeaver {
		return &a2aLeaver{lc: lc, store: f.state.store, members: a2aMembersByContainer(f.state.store)}
	}
	names := []string{"enclave-claude-ab12cd34ef56-worker-1", "enclave-other", "enclave-claude-ab12cd34ef56-lead"}
	code := stopContainers(context.Background(), be, config.DefaultOptions(), names)
	if code != a2aExitDegraded {
		t.Fatalf("code = %d, want 3 for an obligation the reconcile left open", code)
	}
	wantCalls := []string{"leave lead " + string(a2aTestOrchGen), "leave worker-1 " + string(a2aTestWorkerGen), "reconcile review"}
	if !slices.Equal(lc.calls, wantCalls) {
		t.Fatalf("lifecycle calls = %v, want %v", lc.calls, wantCalls)
	}
	wantStopped := []string{"enclave-claude-ab12cd34ef56-lead", "enclave-claude-ab12cd34ef56-worker-1", "enclave-other"}
	if !slices.Equal(be.stopped, wantStopped) {
		t.Fatalf("stopped = %v, want %v", be.stopped, wantStopped)
	}

	// Without peer groups the stop path is unchanged.
	a2aStopLeaver = func(context.Context, backend.Backend, model.Options, []string) *a2aLeaver { return nil }
	plain := &stopTestBackend{}
	if code := stopContainers(context.Background(), plain, config.DefaultOptions(), []string{"b", "a"}); code != 0 || !slices.Equal(plain.stopped, []string{"b", "a"}) {
		t.Fatalf("plain stop: code %d, stopped %v", code, plain.stopped)
	}
}

// reconcilingLifecycle answers reconciles with the given outcome.
type reconcilingLifecycle struct {
	*fakeLifecycle
	open, busy bool
	err        error
}

func (r *reconcilingLifecycle) result(group string) backenddocker.ReconcileResult {
	res := backenddocker.ReconcileResult{Group: group, Busy: r.busy}
	if r.open {
		res.Degraded, res.Problems = true, []string{"worker worker-1 stays quarantined"}
	}
	return res
}

func (r *reconcilingLifecycle) Reconcile(ctx context.Context, group string) (backenddocker.ReconcileResult, error) {
	_, _ = r.fakeLifecycle.Reconcile(ctx, group)
	return r.result(group), r.err
}

func (r *reconcilingLifecycle) ReconcileAll(ctx context.Context) ([]backenddocker.ReconcileResult, error) {
	_, _ = r.fakeLifecycle.ReconcileAll(ctx)
	return []backenddocker.ReconcileResult{r.result("review")}, r.err
}

// TestStopExitReflectsStateAfterReconcile: a degraded leave exits 3 only when the reconcile could not resolve it (I2 B8).
func TestStopExitReflectsStateAfterReconcile(t *testing.T) {
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
			leaver := &a2aLeaver{lc: &lc, store: f.state.store, members: a2aMembersByContainer(f.state.store)}
			leaver.leave(context.Background(), "enclave-claude-ab12cd34ef56-lead")
			code := 0
			if leaver.reconcileLeftGroups(context.Background()) {
				code = a2aExitDegraded
			}
			if code != tc.want {
				t.Fatalf("reconcileLeftGroups exit %d, want %d", code, tc.want)
			}
			code = 0
			if leaver.reconcile(context.Background()) {
				code = a2aExitDegraded
			}
			if code != tc.want {
				t.Fatalf("reconcile exit %d, want %d", code, tc.want)
			}
		})
	}
}

// countA2ALifecycles points the stop path at f's state and counts lifecycle builds.
func countA2ALifecycles(t *testing.T, f *a2aFixture, lc *fakeLifecycle) *int {
	t.Helper()
	builds := 0
	oldHome, oldLifecycle, oldStop := a2aResolveHome, a2aNewLifecycle, a2aStopLeaver
	t.Cleanup(func() { a2aResolveHome, a2aNewLifecycle, a2aStopLeaver = oldHome, oldLifecycle, oldStop })
	a2aResolveHome = func() (string, error) { return f.home, nil }
	a2aNewLifecycle = func(context.Context, model.Options, string) (a2aLifecycle, *backenddocker.Backend, error) {
		builds++
		return lc, nil, nil
	}
	a2aStopLeaver = func(ctx context.Context, _ backend.Backend, opts model.Options, containers []string) *a2aLeaver {
		return newA2AStopLeaver(ctx, opts, containers)
	}
	return &builds
}

func TestPlainRunMakesNoA2ACalls(t *testing.T) {
	f := newA2AFixture(t)
	lc := &fakeLifecycle{}
	builds := countA2ALifecycles(t, f, lc)
	run, err := prepareA2ARun(context.Background(), "run", config.DefaultOptions(), model.Paths{}, model.Host{Home: f.home}, model.Project{}, model.Profile{Name: "claude"}, &stopTestBackend{})
	if err != nil || run != nil {
		t.Fatalf("plain run = %v, %v; want no peer group run", run, err)
	}
	if *builds != 0 || len(lc.calls) != 0 {
		t.Fatalf("plain run: %d lifecycle builds, calls %v", *builds, lc.calls)
	}
}

func TestPlainStopMakesNoA2ACalls(t *testing.T) {
	f := newA2AFixture(t)
	lc := &fakeLifecycle{}
	builds := countA2ALifecycles(t, f, lc)
	be := &stopTestBackend{}
	if code := stopContainers(context.Background(), be, config.DefaultOptions(), []string{"enclave-claude-ab12cd34ef56-plain"}); code != 0 {
		t.Fatalf("plain stop exit %d", code)
	}
	if *builds != 0 || len(lc.calls) != 0 {
		t.Fatalf("plain stop: %d lifecycle builds, calls %v", *builds, lc.calls)
	}
	if !slices.Equal(be.stopped, []string{"enclave-claude-ab12cd34ef56-plain"}) {
		t.Fatalf("stopped = %v", be.stopped)
	}
}

func TestStopMemberReconcilesOnlyItsGroup(t *testing.T) {
	f := newA2AFixture(t)
	if _, err := f.state.store.Create("other"); err != nil {
		t.Fatal(err)
	}
	other := *f.rec
	other.Group = "other"
	other.Members = []peergroup.GroupMember{f.rec.Members[0]}
	other.Members[0].Name = "solo"
	other.Members[0].SessionContainer = "enclave-claude-ab12cd34ef56-solo"
	data, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(peergroup.GroupRecordPath(f.state.stateRoot, "other"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	lc := &fakeLifecycle{}
	builds := countA2ALifecycles(t, f, lc)
	names := []string{"enclave-claude-ab12cd34ef56-plain", "enclave-claude-ab12cd34ef56-worker-2"}
	if code := stopContainers(context.Background(), &stopTestBackend{}, config.DefaultOptions(), names); code != 0 {
		t.Fatalf("member stop exit %d", code)
	}
	want := []string{"leave worker-2 " + string(a2aTestW2Gen), "reconcile review"}
	if *builds != 1 || !slices.Equal(lc.calls, want) {
		t.Fatalf("member stop: %d lifecycle builds, calls %v, want %v", *builds, lc.calls, want)
	}
}

func TestA2AGroupsExist(t *testing.T) {
	if a2aGroupsExist(newA2AState(t.TempDir()).store) {
		t.Fatal("empty state reports groups")
	}
	if !a2aGroupsExist(newA2AFixture(t).state.store) {
		t.Fatal("fixture group not found")
	}
}
