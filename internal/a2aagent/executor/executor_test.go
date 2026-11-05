// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux

package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/a2aagent/contexttable"
	"enclave/internal/a2aagent/executor/fakedriver"
)

func TestMain(m *testing.M) {
	fakedriver.Main()
	os.Exit(m.Run())
}

const terminationTimeout = 300 * time.Millisecond

var (
	orch = a2aagent.PrincipalRef{Member: "orch", Role: a2a.RoleOrchestrator, Generation: "0000000000000001"}
	host = a2aagent.PrincipalRef{Member: "_host", Role: a2a.RoleHost, Generation: "00000000000000aa"}
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// recordingDriver captures every invocation before delegating to the fake driver.
type recordingDriver struct {
	*fakedriver.Driver
	mu   sync.Mutex
	invs []a2aagent.Invocation
}

func (d *recordingDriver) Command(ctx context.Context, inv a2aagent.Invocation) (*exec.Cmd, error) {
	d.mu.Lock()
	d.invs = append(d.invs, inv)
	d.mu.Unlock()
	return d.Driver.Command(ctx, inv)
}

func (d *recordingDriver) invocations() []a2aagent.Invocation {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]a2aagent.Invocation{}, d.invs...)
}

func (d *recordingDriver) setMode(mode string) {
	d.mu.Lock()
	d.Mode = mode
	d.mu.Unlock()
}

type harness struct {
	t       *testing.T
	dir     string
	tbl     *contexttable.Table
	ex      *Executor
	drv     *recordingDriver
	log     *syncBuffer
	phantom atomic.Bool
	noKill  atomic.Bool
	cancel  context.CancelFunc
	done    chan struct{}
}

type setup struct {
	mode   string
	tweak  func(*Options)
	before func(h *harness)
	dir    string
}

func newHarness(t *testing.T, s setup) *harness {
	t.Helper()
	h := &harness{t: t, dir: s.dir, log: &syncBuffer{}}
	if h.dir == "" {
		h.dir = t.TempDir()
	}
	h.tbl = openTable(t, h.dir)
	if s.before != nil {
		s.before(h)
	}
	h.drv = &recordingDriver{Driver: &fakedriver.Driver{Exe: os.Args[0], Mode: s.mode}}
	opts := Options{
		Table: h.tbl, Driver: h.drv,
		Group: "review", GroupGeneration: "00000000000000ff", Worker: "worker-1", WorkerGeneration: "0000000000000abc",
		WorkDir: t.TempDir(), TaskDeadline: 20 * time.Second, TerminationTimeout: terminationTimeout,
		PollInterval: 20 * time.Millisecond, Log: h.log,
	}
	if s.tweak != nil {
		s.tweak(&opts)
	}
	ex, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.ex = ex
	// Fault injection: a phantom descendant that never dies, and signals that never arrive.
	realScan, realGroup, realProc := ex.procs.scan, ex.procs.signalGroup, ex.procs.signalProcess
	self := ex.procs.self
	ex.procs.scan = func() ([]procInfo, error) {
		procs, err := realScan()
		if h.phantom.Load() {
			procs = append(procs, procInfo{PID: 1 << 30, PPID: self, PGID: 1 << 30})
		}
		return procs, err
	}
	ex.procs.signalGroup = func(pgid int, sig signal) error {
		if h.noKill.Load() {
			return nil
		}
		return realGroup(pgid, sig)
	}
	ex.procs.signalProcess = func(pid int, sig signal) error {
		if h.noKill.Load() {
			return nil
		}
		return realProc(pid, sig)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan struct{})
	go func() {
		defer close(h.done)
		if err := ex.Run(ctx); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	t.Cleanup(func() {
		h.phantom.Store(false)
		h.noKill.Store(false)
		cancel()
		<-h.done
		if live, _ := ex.liveDescendants(); len(live) > 0 {
			t.Errorf("descendants left after the test: %v", live)
			for _, pid := range live {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		if t.Failed() {
			t.Logf("executor log:\n%s", h.log.String())
		}
	})
	return h
}

func openTable(t *testing.T, dir string) *contexttable.Table {
	t.Helper()
	tbl, err := contexttable.Open(contexttable.Options{
		Dir: dir,
		Header: a2aagent.TableHeader{
			Group: "review", GroupGeneration: "00000000000000ff", Worker: "worker-1", WorkerGeneration: "0000000000000abc",
		},
	})
	if err != nil {
		t.Fatalf("Open: %s", describe(err))
	}
	return tbl
}

var msgSeq atomic.Int64

func admit(t *testing.T, tbl *contexttable.Table, p a2aagent.PrincipalRef, contextID string) *a2aagent.TaskRecord {
	t.Helper()
	adm, err := tbl.Admit(context.Background(), a2aagent.AdmitRequest{
		Principal: p, Epoch: "e1", MessageID: fmt.Sprintf("m%d", msgSeq.Add(1)),
		ContentHash: "sha256:00", ContextID: contextID,
	})
	if err != nil {
		t.Fatalf("Admit: %s", describe(err))
	}
	return adm.Task
}

func (h *harness) send(p a2aagent.PrincipalRef, contextID, text string) *a2aagent.TaskRecord {
	h.t.Helper()
	rec := admit(h.t, h.tbl, p, contextID)
	h.ex.Submit(rec.TaskID, text)
	return rec
}

func (h *harness) waitFor(taskID string, what string, ok func(*a2aagent.TaskRecord) bool) *a2aagent.TaskRecord {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		rec, err := h.tbl.LookupTask(taskID)
		if err == nil && ok(rec) {
			return rec
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("task %s: timed out waiting for %s (last %+v)", taskID, what, rec)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) waitWorking(taskID string) *a2aagent.TaskRecord {
	h.t.Helper()
	return h.waitFor(taskID, "working with a pgid", func(r *a2aagent.TaskRecord) bool {
		return r.State == a2a.TaskStateWorking && r.PGID > 0
	})
}

func (h *harness) waitEnded(taskID string) *a2aagent.TaskRecord {
	h.t.Helper()
	return h.waitFor(taskID, "a terminal state", func(r *a2aagent.TaskRecord) bool {
		return r.State != a2a.TaskStateSubmitted && r.State != a2a.TaskStateWorking
	})
}

type ending struct {
	state       a2a.TaskState
	reason      a2a.EndingReason
	termination a2a.Termination
}

func wantEnding(t *testing.T, rec *a2aagent.TaskRecord, want ending) {
	t.Helper()
	got := ending{rec.State, rec.Reason, rec.Termination}
	if got != want {
		t.Fatalf("task %s ended %+v, want %+v", rec.TaskID, got, want)
	}
	if rec.EndedAt == "" {
		t.Fatalf("task %s has no endedAt", rec.TaskID)
	}
}

func (h *harness) wantQuarantine(taskID string) {
	h.t.Helper()
	r, _ := h.tbl.Restrictions()
	if r.Quarantine == nil || r.Quarantine.TaskID != taskID || r.Quarantine.Reason != QuarantineReason {
		h.t.Fatalf("quarantine: %+v, want task %s", r.Quarantine, taskID)
	}
	_, err := h.tbl.Admit(context.Background(), a2aagent.AdmitRequest{Principal: host, MessageID: "after-quarantine", ContentHash: "sha256:00"})
	wantCode(h.t, err, a2a.CodeQuarantined)
}

func (h *harness) wantNoQuarantine() {
	h.t.Helper()
	if r, _ := h.tbl.Restrictions(); r.Quarantine != nil {
		h.t.Fatalf("unexpected quarantine: %+v", r.Quarantine)
	}
}

func (h *harness) wantTreeEmpty() {
	h.t.Helper()
	alive, err := h.ex.DescendantsAlive()
	if err != nil || alive {
		h.t.Fatalf("DescendantsAlive = %v, %v; want an empty tree", alive, err)
	}
}

func wantCode(t *testing.T, err error, code int) {
	t.Helper()
	var ae *a2a.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("want a2a error %d, got %s", code, describeOrNil(err))
	}
}

func describeOrNil(err error) string {
	if err == nil {
		return "<nil>"
	}
	return describe(err)
}

func groupAlive(pgid int) bool {
	return syscall.Kill(-pgid, 0) == nil
}

func TestCompletesAndBindsSession(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeComplete})
	first := admit(t, h.tbl, orch, "")
	events, _ := h.ex.Subscribe(first.TaskID)
	h.ex.Submit(first.TaskID, "hello")
	rec := h.waitEnded(first.TaskID)
	wantEnding(t, rec, ending{a2a.TaskStateCompleted, "", a2a.TerminationConfirmed})
	header := fmt.Sprintf("[a2a group=review from=orch role=orchestrator generation=0000000000000001 task=%s context=%s]", rec.TaskID, rec.ContextID)
	if rec.Result != "echo:"+header+"\n\nhello" || rec.Truncated {
		t.Fatalf("result %q", rec.Result)
	}
	if rec.PGID <= 0 || rec.StartedAt == "" || rec.DeadlineAt == "" {
		t.Fatalf("execution fields not recorded: %+v", rec)
	}
	ctxRec, err := h.tbl.Context(orch, rec.ContextID)
	if err != nil || ctxRec.HarnessSessionID != fakedriver.SessionID || len(ctxRec.Transcripts) != 1 {
		t.Fatalf("session binding: %+v %v", ctxRec, err)
	}

	var got []a2aagent.TaskEvent
	for ev := range events {
		got = append(got, ev)
	}
	if len(got) != 3 || got[0].Event.StatusUpdate == nil || got[0].Event.StatusUpdate.Status.State != a2a.TaskStateWorking ||
		got[1].Event.ArtifactUpdate == nil || *got[1].Event.ArtifactUpdate.Artifact.Parts[0].Text != rec.Result ||
		got[2].Event.StatusUpdate == nil || got[2].Event.StatusUpdate.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("events: %+v", got)
	}
	meta, _ := got[2].Event.StatusUpdate.Metadata[a2a.ExtensionURI].(a2a.EnclaveMeta)
	if meta.Termination != a2a.TerminationConfirmed {
		t.Fatalf("terminal metadata: %+v", got[2].Event.StatusUpdate.Metadata)
	}

	// A follow-up turn in the same context resumes the bound harness session.
	second := h.send(orch, rec.ContextID, "again")
	h.waitEnded(second.TaskID)
	invs := h.drv.invocations()
	if len(invs) != 2 || invs[0].HarnessSessionID != "" || invs[1].HarnessSessionID != fakedriver.SessionID {
		t.Fatalf("invocations: %+v", invs)
	}

	// A subscription to an ended task replays the ending and closes.
	replay, _ := h.ex.Subscribe(first.TaskID)
	n := 0
	for range replay {
		n++
	}
	if n != 2 {
		t.Fatalf("replayed %d events, want artifact and status", n)
	}
	h.wantTreeEmpty()
}

func TestHarnessEnvironmentHasNoA2AVariables(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeEnv, tweak: func(o *Options) {
		o.Env = []string{"PATH=" + os.Getenv("PATH"), "ENCLAVE_A2A_INBOUND_TOKEN=secret", "ENCLAVE_A2A_OUTBOUND_TOKEN=secret", "KEEP=1"}
	}})
	rec := h.waitEnded(h.send(orch, "", "x").TaskID)
	if rec.Result != "env:" {
		t.Fatalf("harness saw A2A variables: %q", rec.Result)
	}
	for _, kv := range h.drv.invocations()[0].Env {
		if strings.HasPrefix(kv, "ENCLAVE_A2A_") {
			t.Fatalf("invocation env carries %s", kv)
		}
	}
}

func TestHarnessFailures(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeFail})
	rec := h.waitEnded(h.send(orch, "", "x").TaskID)
	wantEnding(t, rec, ending{a2a.TaskStateFailed, "", a2a.TerminationConfirmed})

	h.drv.setMode(fakedriver.ModeUnknownEvent)
	rec = h.waitEnded(h.send(orch, "", "x").TaskID)
	wantEnding(t, rec, ending{a2a.TaskStateFailed, "", a2a.TerminationConfirmed})
	h.wantNoQuarantine()
}

func TestResultTruncated(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeBig, tweak: func(o *Options) { o.MaxResultBytes = 100 }})
	h.drv.Env = []string{fakedriver.EnvSize + "=1000"}
	task := admit(t, h.tbl, orch, "")
	events, _ := h.ex.Subscribe(task.TaskID)
	h.ex.Submit(task.TaskID, "x")
	rec := h.waitEnded(task.TaskID)
	if len(rec.Result) != 100 || !rec.Truncated || rec.State != a2a.TaskStateCompleted || !strings.HasSuffix(rec.Result, a2a.TruncatedMarker) {
		t.Fatalf("result %d bytes %q, truncated %v, state %s", len(rec.Result), rec.Result, rec.Truncated, rec.State)
	}
	var last a2aagent.TaskEvent
	for ev := range events {
		last = ev
	}
	meta, _ := last.Event.StatusUpdate.Metadata[a2a.ExtensionURI].(a2a.EnclaveMeta)
	if !meta.Truncated {
		t.Fatalf("terminal metadata: %+v", last.Event.StatusUpdate.Metadata)
	}
}

func TestEmptyReplyCompletesWithEmptyArtifact(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeEmpty})
	task := admit(t, h.tbl, orch, "")
	events, _ := h.ex.Subscribe(task.TaskID)
	h.ex.Submit(task.TaskID, "x")
	rec := h.waitEnded(task.TaskID)
	wantEnding(t, rec, ending{a2a.TaskStateCompleted, "", a2a.TerminationConfirmed})
	var artifacts []a2a.Artifact
	for ev := range events {
		if ev.Event.ArtifactUpdate != nil {
			artifacts = append(artifacts, ev.Event.ArtifactUpdate.Artifact)
		}
	}
	if len(artifacts) != 1 || len(artifacts[0].Parts) != 1 || artifacts[0].Parts[0].Text == nil || *artifacts[0].Parts[0].Text != "" {
		t.Fatalf("artifacts: %+v", artifacts)
	}
}

func TestNewSessionIsBoundBeforeStart(t *testing.T) {
	const preBound = "pre-bound-session"
	h := newHarness(t, setup{mode: fakedriver.ModeCrash})
	h.drv.NewSession = preBound
	rec := h.waitEnded(h.send(orch, "", "x").TaskID)
	wantEnding(t, rec, ending{a2a.TaskStateFailed, "", a2a.TerminationConfirmed})
	ctxRec, err := h.tbl.Context(orch, rec.ContextID)
	if err != nil || ctxRec.HarnessSessionID != preBound {
		t.Fatalf("binding after a crash before init: %+v %v", ctxRec, err)
	}

	// The harness never reported the session, so the next turn creates it instead of resuming.
	h.drv.setMode(fakedriver.ModeComplete)
	h.waitEnded(h.send(orch, rec.ContextID, "again").TaskID)
	h.waitEnded(h.send(orch, rec.ContextID, "third").TaskID)
	invs := h.drv.invocations()
	if len(invs) != 3 ||
		invs[0].HarnessSessionID != "" || invs[0].NewHarnessSessionID != preBound ||
		invs[1].HarnessSessionID != "" || invs[1].NewHarnessSessionID != preBound ||
		invs[2].HarnessSessionID != preBound || invs[2].NewHarnessSessionID != "" {
		t.Fatalf("invocations: %+v", invs)
	}
}

type inventoryDriver struct {
	a2aagent.Driver
	paths []string
}

func (d inventoryDriver) InvocationTranscripts(a2aagent.Invocation) []string { return d.paths }

type inventoryFailTable struct{ *contexttable.Table }

func (t inventoryFailTable) BindHarnessSession(contextID, sessionID string, paths []string) error {
	if len(paths) > 0 {
		return errors.New("inventory write failed")
	}
	return t.Table.BindHarnessSession(contextID, sessionID, paths)
}

func TestInvocationInventorySurvivesCrashBeforeAnyEvent(t *testing.T) {
	for _, preBound := range []string{"", "pre-bound-session"} {
		t.Run("session="+preBound, func(t *testing.T) {
			h := newHarness(t, setup{mode: fakedriver.ModeCrash, tweak: func(opts *Options) {
				opts.Driver = inventoryDriver{Driver: opts.Driver, paths: []string{"private/turn-1"}}
			}})
			h.drv.NewSession = preBound
			rec := h.waitEnded(h.send(orch, "", "x").TaskID)
			wantEnding(t, rec, ending{a2a.TaskStateFailed, "", a2a.TerminationConfirmed})
			ctxRec, err := h.tbl.Context(orch, rec.ContextID)
			if err != nil || len(ctxRec.Transcripts) != 1 || ctxRec.Transcripts[0] != "private/turn-1" {
				t.Fatalf("inventory lost before first harness event: %+v %v", ctxRec, err)
			}
		})
	}
}

func TestInventoryPersistenceFailurePreventsHarnessStart(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeComplete, tweak: func(opts *Options) {
		opts.Table = inventoryFailTable{opts.Table.(*contexttable.Table)}
		opts.Driver = inventoryDriver{Driver: opts.Driver, paths: []string{"private/turn-1"}}
	}})
	rec := h.waitEnded(h.send(orch, "", "x").TaskID)
	if rec.State != a2a.TaskStateFailed || rec.PGID != 0 || !strings.Contains(h.log.String(), "persist harness inventory") {
		t.Fatalf("harness started without inventory: %+v; log %s", rec, h.log.String())
	}
}

func TestTasksRunSerially(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeSlow})
	first := h.send(orch, "", "1")
	second := h.send(host, "", "2")
	h.waitWorking(first.TaskID)
	if rec, _ := h.tbl.LookupTask(second.TaskID); rec.State != a2a.TaskStateSubmitted {
		t.Fatalf("second task %s while the first runs", rec.State)
	}
	h.waitEnded(first.TaskID)
	if rec := h.waitEnded(second.TaskID); rec.State != a2a.TaskStateCompleted {
		t.Fatalf("second task: %+v", rec)
	}
}

// Proposal 12 row 1: explicit cancel, process group killed and confirmed.
func TestCancelRunningConfirmed(t *testing.T) {
	for _, mode := range []string{fakedriver.ModeHang, fakedriver.ModeIgnoreTerm} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, setup{mode: mode})
			rec := h.send(orch, "", "x")
			running := h.waitWorking(rec.TaskID)
			start := time.Now()
			got, err := h.ex.Cancel(context.Background(), rec.TaskID)
			if err != nil {
				t.Fatalf("Cancel: %s", describe(err))
			}
			t.Logf("%s: cancel confirmed after %s", mode, time.Since(start).Round(time.Millisecond))
			wantEnding(t, got, ending{a2a.TaskStateCanceled, a2a.ReasonCancel, a2a.TerminationConfirmed})
			if groupAlive(running.PGID) {
				t.Fatal("process group still alive after a confirmed cancel")
			}
			h.wantNoQuarantine()
			h.wantTreeEmpty()
		})
	}
}

// CR-P2-1: a setsid child and a double-forked grandchild leave the process
// group; the descendant tree sweep still catches them.
func TestCancelCatchesDescendantsOutsideTheGroup(t *testing.T) {
	for _, mode := range []string{fakedriver.ModeEscape, fakedriver.ModeDoubleFork} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, setup{mode: mode})
			rec := h.send(orch, "", "x")
			h.waitWorking(rec.TaskID)
			waitForTreeSize(t, h.ex, 2)
			got, err := h.ex.Cancel(context.Background(), rec.TaskID)
			if err != nil {
				t.Fatalf("Cancel: %s", describe(err))
			}
			wantEnding(t, got, ending{a2a.TaskStateCanceled, a2a.ReasonCancel, a2a.TerminationConfirmed})
			h.wantNoQuarantine()
			h.wantTreeEmpty()
		})
	}
}

// waitForTreeSize waits until the harness has started its children, so a cancel cannot race them.
func waitForTreeSize(t *testing.T, ex *Executor, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		live, _ := ex.liveDescendants()
		if len(live) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant tree reached %d processes, want %d", len(live), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// CR-P2-1: leftovers after a successful run are swept before the next task.
func TestSweepAfterSuccess(t *testing.T) {
	for _, mode := range []string{fakedriver.ModeChildThenExit, fakedriver.ModeEscapeThenExit, fakedriver.ModeDoubleForkThenExit} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, setup{mode: mode})
			first := h.send(orch, "", "x")
			second := admit(t, h.tbl, orch, "")
			rec := h.waitEnded(first.TaskID)
			wantEnding(t, rec, ending{a2a.TaskStateCompleted, "", a2a.TerminationConfirmed})
			if rec.Result == "" {
				t.Fatal("final message lost")
			}
			h.wantTreeEmpty()
			h.drv.setMode(fakedriver.ModeComplete)
			h.ex.Submit(second.TaskID, "next")
			if got := h.waitEnded(second.TaskID); got.State != a2a.TaskStateCompleted {
				t.Fatalf("next task: %+v", got)
			}
		})
	}
}

// Proposal 12 row 2: explicit cancel, kill not confirmed within the bound.
func TestCancelUnconfirmedQuarantines(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeHang})
	rec := h.send(orch, "", "x")
	running := h.waitWorking(rec.TaskID)
	queued := h.send(host, "", "y")
	h.phantom.Store(true)
	start := time.Now()
	got, err := h.ex.Cancel(context.Background(), rec.TaskID)
	if err != nil {
		t.Fatalf("Cancel: %s", describe(err))
	}
	t.Logf("unconfirmed after %s", time.Since(start).Round(time.Millisecond))
	wantEnding(t, got, ending{a2a.TaskStateFailed, a2a.ReasonCancel, a2a.TerminationUnconfirmed})
	h.wantQuarantine(rec.TaskID)
	if r, _ := h.tbl.Restrictions(); r.Quarantine.PGID != running.PGID {
		t.Fatalf("quarantine pgid %d, want %d", r.Quarantine.PGID, running.PGID)
	}
	if alive, _ := h.ex.DescendantsAlive(); !alive {
		t.Fatal("DescendantsAlive must report the surviving descendant")
	}

	// Nothing is dequeued while quarantined; host reconcile releases it once the tree is empty.
	time.Sleep(100 * time.Millisecond)
	if q, _ := h.tbl.LookupTask(queued.TaskID); q.State != a2a.TaskStateSubmitted {
		t.Fatalf("task dequeued under quarantine: %s", q.State)
	}
	h.phantom.Store(false)
	h.wantTreeEmpty()
	if err := h.tbl.SetQuarantine(nil); err != nil {
		t.Fatal(err)
	}
	h.waitWorking(queued.TaskID)
	if _, err := h.ex.Cancel(context.Background(), queued.TaskID); err != nil {
		t.Fatal(describe(err))
	}
}

// Proposal 12 rows 3 and 6: a caller wait timeout or an unreachable hub
// changes nothing at the adapter; dropping a subscription never cancels.
func TestDroppedSubscriptionDoesNotCancel(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeSlow})
	rec := admit(t, h.tbl, orch, "")
	_, unsubscribe := h.ex.Subscribe(rec.TaskID)
	h.ex.Submit(rec.TaskID, "x")
	h.waitWorking(rec.TaskID)
	unsubscribe()
	unsubscribe()
	got := h.waitEnded(rec.TaskID)
	wantEnding(t, got, ending{a2a.TaskStateCompleted, "", a2a.TerminationConfirmed})
}

// Proposal 12 row 4: execution deadline reached, killed and confirmed.
func TestDeadline(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeHang, tweak: func(o *Options) { o.TaskDeadline = 200 * time.Millisecond }})
	rec := h.waitEnded(h.send(orch, "", "x").TaskID)
	wantEnding(t, rec, ending{a2a.TaskStateFailed, a2a.ReasonDeadline, a2a.TerminationConfirmed})
	h.wantTreeEmpty()
}

// Proposal 12 row 5: reconnect window elapsed at the adapter, killed and confirmed.
func TestHubLost(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeHang})
	running := h.send(orch, "", "x")
	h.waitWorking(running.TaskID)
	queued := h.send(orch, "", "y")
	if err := h.ex.HubLost(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantEnding(t, h.waitEnded(running.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonHubLost, a2a.TerminationConfirmed})
	wantEnding(t, h.waitEnded(queued.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonHubLost, a2a.TerminationNotApplicable})
	h.wantTreeEmpty()
}

// Proposal 12 row 7: hub epoch changed while running or queued.
func TestEpochChanged(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		h := newHarness(t, setup{mode: fakedriver.ModeHang})
		running := h.send(orch, "", "x")
		h.waitWorking(running.TaskID)
		queued := h.send(orch, "", "y")
		if err := h.ex.EpochChanged(context.Background(), "e2"); err != nil {
			t.Fatal(err)
		}
		wantEnding(t, h.waitEnded(running.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonEpochChanged, a2a.TerminationConfirmed})
		wantEnding(t, h.waitEnded(queued.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonEpochChanged, a2a.TerminationNotApplicable})

		// A task admitted under the old epoch after the change is ended at dequeue.
		late := h.send(orch, "", "z")
		wantEnding(t, h.waitEnded(late.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonEpochChanged, a2a.TerminationNotApplicable})
		h.wantNoQuarantine()
	})
	t.Run("unconfirmed", func(t *testing.T) {
		h := newHarness(t, setup{mode: fakedriver.ModeHang})
		running := h.send(orch, "", "x")
		h.waitWorking(running.TaskID)
		h.phantom.Store(true)
		if err := h.ex.EpochChanged(context.Background(), "e2"); err != nil {
			t.Fatal(err)
		}
		wantEnding(t, h.waitEnded(running.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonEpochChanged, a2a.TerminationUnconfirmed})
		h.wantQuarantine(running.TaskID)
	})
}

// Proposal 12 row 8: queued tasks discarded on suspected departure; running work continues.
func TestSuspectedDepartureDiscardsQueued(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeHang})
	running := h.send(orch, "", "x")
	h.waitWorking(running.TaskID)
	queued := h.send(orch, "", "y")
	other := h.send(host, "", "z")
	ack, err := h.ex.Depart(context.Background(), orch, false)
	if err != nil || !ack.AdmissionStopPersisted || len(ack.QueuedDiscarded) != 1 || ack.QueuedDiscarded[0] != queued.TaskID ||
		len(ack.RunningTerminated) != 0 || ack.Termination != a2a.TerminationNotApplicable {
		t.Fatalf("suspected departure ack: %+v %v", ack, err)
	}
	wantEnding(t, h.waitEnded(queued.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonOrchestratorLost, a2a.TerminationNotApplicable})
	time.Sleep(100 * time.Millisecond)
	if rec, _ := h.tbl.LookupTask(running.TaskID); rec.State != a2a.TaskStateWorking {
		t.Fatalf("running task ended on a suspected departure: %+v", rec)
	}
	if rec, _ := h.tbl.LookupTask(other.TaskID); rec.State != a2a.TaskStateSubmitted {
		t.Fatalf("another principal's task was touched: %+v", rec)
	}
	r, _ := h.tbl.Restrictions()
	if len(r.AdmissionStops) != 1 || r.AdmissionStops[0].Reason != contexttable.StopSuspected {
		t.Fatalf("admission stops: %+v", r.AdmissionStops)
	}
	_, err = h.tbl.Admit(context.Background(), a2aagent.AdmitRequest{Principal: orch, MessageID: "late", ContentHash: "sha256:00"})
	wantCode(t, err, a2a.CodeAdmissionStopped)
	if _, err := h.ex.Cancel(context.Background(), running.TaskID); err != nil {
		t.Fatal(describe(err))
	}
	if _, err := h.ex.Cancel(context.Background(), other.TaskID); err != nil {
		t.Fatal(describe(err))
	}
}

// Proposal 12 row 9: running task terminated on authoritative departure.
func TestAuthoritativeDeparture(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		h := newHarness(t, setup{mode: fakedriver.ModeHang})
		running := h.send(orch, "", "x")
		h.waitWorking(running.TaskID)
		queued := h.send(orch, "", "y")
		ack, err := h.ex.Depart(context.Background(), orch, true)
		if err != nil {
			t.Fatal(err)
		}
		if !ack.AdmissionStopPersisted || len(ack.QueuedDiscarded) != 1 || ack.QueuedDiscarded[0] != queued.TaskID ||
			len(ack.RunningTerminated) != 1 || ack.RunningTerminated[0] != running.TaskID ||
			ack.Termination != a2a.TerminationConfirmed || ack.Worker != "worker-1" || ack.WorkerGeneration != "0000000000000abc" ||
			ack.Principal.Member != "orch" || ack.Principal.Generation != orch.Generation || ack.Principal.Group != "review" || ack.At == "" {
			t.Fatalf("ack: %+v", ack)
		}
		wantEnding(t, h.waitEnded(running.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonOrchestratorLost, a2a.TerminationConfirmed})
		wantEnding(t, h.waitEnded(queued.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonOrchestratorLost, a2a.TerminationNotApplicable})
		r, _ := h.tbl.Restrictions()
		if len(r.AdmissionStops) != 1 || r.AdmissionStops[0].Reason != contexttable.StopAuthoritative {
			t.Fatalf("admission stops: %+v", r.AdmissionStops)
		}

		// Nothing running for the principal: the ack carries not-applicable.
		ack, err = h.ex.Depart(context.Background(), orch, true)
		if err != nil || ack.Termination != a2a.TerminationNotApplicable || len(ack.RunningTerminated) != 0 {
			t.Fatalf("repeat ack: %+v %v", ack, err)
		}
	})
	t.Run("unconfirmed", func(t *testing.T) {
		h := newHarness(t, setup{mode: fakedriver.ModeHang})
		running := h.send(orch, "", "x")
		h.waitWorking(running.TaskID)
		h.phantom.Store(true)
		ack, err := h.ex.Depart(context.Background(), orch, true)
		if err != nil || ack.Termination != a2a.TerminationUnconfirmed {
			t.Fatalf("ack: %+v %v", ack, err)
		}
		wantEnding(t, h.waitEnded(running.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonOrchestratorLost, a2a.TerminationUnconfirmed})
		h.wantQuarantine(running.TaskID)
	})
}

// startSurvivor plays a harness left behind by a previous adapter process.
func startSurvivor(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0]) // #nosec G204 G702 -- re-executes the test binary.
	cmd.Env = append(os.Environ(), fakedriver.EnvMode+"="+fakedriver.ModeSleep)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waited)
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waited
	})
	return cmd
}

func markWorking(t *testing.T, tbl *contexttable.Table, rec *a2aagent.TaskRecord, pgid int) {
	t.Helper()
	rec.State = a2a.TaskStateWorking
	rec.PGID = pgid
	rec.StartedAt = "2026-11-05T12:00:00Z"
	if err := tbl.UpdateTask(rec); err != nil {
		t.Fatal(err)
	}
}

// Proposal 12 row 10: adapter restarted with a surviving subprocess.
func TestRestartWithSurvivingSubprocess(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		dir := t.TempDir()
		survivor := startSurvivor(t)
		var stale, orphan *a2aagent.TaskRecord
		h := newHarness(t, setup{mode: fakedriver.ModeComplete, dir: dir, before: func(h *harness) {
			stale = admit(t, h.tbl, orch, "")
			markWorking(t, h.tbl, stale, survivor.Process.Pid)
			orphan = admit(t, h.tbl, orch, "")
		}})
		// Queued work from the previous run lost its message text and is failed at New.
		wantEnding(t, h.waitEnded(orphan.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonOutcomeUnknown, a2a.TerminationNotApplicable})
		wantEnding(t, h.waitEnded(stale.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonOutcomeUnknown, a2a.TerminationConfirmed})
		if groupAlive(survivor.Process.Pid) {
			t.Fatal("surviving process group not killed")
		}
		h.wantNoQuarantine()
		// Replay is rejected: the context continues only with new work.
		next := h.send(orch, stale.ContextID, "new work")
		if got := h.waitEnded(next.TaskID); got.State != a2a.TaskStateCompleted {
			t.Fatalf("new work after recovery: %+v", got)
		}
	})
	t.Run("unconfirmed", func(t *testing.T) {
		survivor := startSurvivor(t)
		var stale *a2aagent.TaskRecord
		h := newHarness(t, setup{mode: fakedriver.ModeComplete, before: func(h *harness) {
			stale = admit(t, h.tbl, orch, "")
			markWorking(t, h.tbl, stale, survivor.Process.Pid)
			h.noKill.Store(true)
		}})
		wantEnding(t, h.waitEnded(stale.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonOutcomeUnknown, a2a.TerminationUnconfirmed})
		h.wantQuarantine(stale.TaskID)
		if alive, _ := h.ex.DescendantsAlive(); !alive {
			t.Fatal("DescendantsAlive must report the surviving group")
		}
		h.noKill.Store(false)
		_ = syscall.Kill(-survivor.Process.Pid, syscall.SIGKILL)
		deadline := time.Now().Add(5 * time.Second)
		for {
			alive, _ := h.ex.DescendantsAlive()
			if !alive {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("DescendantsAlive stays true after the group died")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	t.Run("crash before the pgid was recorded", func(t *testing.T) {
		var stale *a2aagent.TaskRecord
		h := newHarness(t, setup{mode: fakedriver.ModeComplete, before: func(h *harness) {
			stale = admit(t, h.tbl, orch, "")
			markWorking(t, h.tbl, stale, 0)
		}})
		wantEnding(t, h.waitEnded(stale.TaskID), ending{a2a.TaskStateFailed, a2a.ReasonOutcomeUnknown, a2a.TerminationUnconfirmed})
		h.wantQuarantine(stale.TaskID)
	})
}

func TestCancelQueuedAndEnded(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeComplete})
	queued := admit(t, h.tbl, orch, "")
	got, err := h.ex.Cancel(context.Background(), queued.TaskID)
	if err != nil {
		t.Fatal(describe(err))
	}
	wantEnding(t, got, ending{a2a.TaskStateCanceled, a2a.ReasonCancel, a2a.TerminationNotApplicable})
	h.ex.Submit(queued.TaskID, "too late")
	time.Sleep(60 * time.Millisecond)
	if rec, _ := h.tbl.LookupTask(queued.TaskID); rec.State != a2a.TaskStateCanceled {
		t.Fatalf("canceled task ran: %+v", rec)
	}
	_, err = h.ex.Cancel(context.Background(), queued.TaskID)
	wantCode(t, err, a2a.CodeTaskNotCancelable)
	_, err = h.ex.Cancel(context.Background(), "missing")
	wantCode(t, err, a2a.CodeTaskNotFound)
}

func TestShutdownEndsRunningTask(t *testing.T) {
	h := newHarness(t, setup{mode: fakedriver.ModeHang})
	rec := h.send(orch, "", "x")
	h.waitWorking(rec.TaskID)
	h.cancel()
	<-h.done
	got, _ := h.tbl.LookupTask(rec.TaskID)
	wantEnding(t, got, ending{a2a.TaskStateFailed, a2a.ReasonOutcomeUnknown, a2a.TerminationConfirmed})
}

func TestHeartbeat(t *testing.T) {
	var ticks atomic.Int64
	base := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, setup{mode: fakedriver.ModeComplete, tweak: func(o *Options) {
		o.HeartbeatInterval = 20 * time.Millisecond
		o.Now = func() time.Time { return base.Add(time.Duration(ticks.Add(1)) * time.Minute) }
	}})
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, _ := h.tbl.Restrictions()
		if r.UpdatedAt > "2026-11-05T12:02:00Z" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat did not advance updatedAt: %s", r.UpdatedAt)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestParseStat(t *testing.T) {
	info, ok := parseStat(42, "42 (we ird) (name)) S 7 42 42 0 -1")
	if !ok || info.PPID != 7 || info.PGID != 42 || info.Zombie {
		t.Fatalf("parseStat: %+v %v", info, ok)
	}
	if info, ok := parseStat(9, "9 (z) Z 1 9 9"); !ok || !info.Zombie {
		t.Fatalf("zombie: %+v", info)
	}
	threaded := "10 (t) Z 1 10 10 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 3 0"
	if info, ok := parseStat(10, threaded); !ok || info.Zombie {
		t.Fatalf("zombie leader with live threads must count as live: %+v %v", info, ok)
	}
	if info, ok := parseStat(11, "11 (t) Z 1 11 11 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0"); !ok || !info.Zombie {
		t.Fatalf("single-threaded zombie: %+v %v", info, ok)
	}
	if _, ok := parseStat(1, "garbage"); ok {
		t.Fatal("garbage parsed")
	}
}
