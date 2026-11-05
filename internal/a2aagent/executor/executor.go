// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package executor runs admitted A2A tasks one at a time, each harness run in
// its own process group, and implements a2aagent.Executor (proposal 9, 12, 13).
package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/a2aagent/contexttable"
)

// Defaults from proposal 17, used when an Options bound is zero.
const (
	DefaultTaskDeadline       = 30 * time.Minute
	DefaultTerminationTimeout = 10 * time.Second
	DefaultMaxResultBytes     = 256 << 10
	DefaultPollInterval       = time.Second
)

// QuarantineReason is the restrictions.json quarantine reason the executor writes.
const QuarantineReason = "termination-unconfirmed"

// ResultArtifactID names the artifact carrying the final message.
const ResultArtifactID = "result"

// Table is the context table the executor needs: the interface plus unbound
// lookups, because Cancel and recovery act on task IDs the server already authorized.
type Table interface {
	a2aagent.ContextTable
	LookupTask(taskID string) (*a2aagent.TaskRecord, error)
	Tasks() []*a2aagent.TaskRecord
}

var _ Table = (*contexttable.Table)(nil)

// Options configures an Executor.
type Options struct {
	Table  Table
	Driver a2aagent.Driver

	Group            string
	GroupGeneration  string
	Worker           string
	WorkerGeneration string

	WorkDir string
	// Env is the harness base environment; nil uses os.Environ(). ENCLAVE_A2A_* is always removed.
	Env []string

	TaskDeadline       time.Duration
	TerminationTimeout time.Duration
	MaxResultBytes     int
	// HeartbeatInterval > 0 makes Run call Table.Heartbeat and Table.Expire on that interval.
	HeartbeatInterval time.Duration
	// PollInterval bounds how long Run sleeps without a wake-up.
	PollInterval time.Duration

	// Log receives harness stderr and executor diagnostics; nil discards them.
	Log io.Writer
	Now func() time.Time
}

type signal int

const (
	sigTerm signal = 15
	sigKill signal = 9
)

type procInfo struct {
	PID, PPID, PGID int
	Zombie          bool
}

// processOps is the process-control seam; tests replace members to simulate unkillable groups.
type processOps struct {
	signalGroup   func(pgid int, sig signal) error
	signalProcess func(pid int, sig signal) error
	scan          func() ([]procInfo, error)
	reap          func(pid int)
	self          int
}

// Executor implements a2aagent.Executor.
type Executor struct {
	opts  Options
	procs processOps

	mu      sync.Mutex
	epoch   string
	running *run
	texts   map[string]string
	subs    map[string][]*subscriber
	active  map[int]bool
	// pending maps a context to its pre-bound session ID until the harness reports it (CR-A9-1).
	pending map[string]string
	started bool
	wake    chan struct{}
}

var _ a2aagent.Executor = (*Executor)(nil)

type run struct {
	taskID    string
	principal a2aagent.PrincipalRef
	epoch     string
	text      string

	stopOnce sync.Once
	stop     chan struct{}
	reason   a2a.EndingReason

	done  chan struct{}
	final *a2aagent.TaskRecord
}

func (r *run) requestStop(reason a2a.EndingReason) {
	r.stopOnce.Do(func() {
		r.reason = reason
		close(r.stop)
	})
}

type subscriber struct {
	ch     chan a2aagent.TaskEvent
	closed bool
}

// New creates an executor. Tasks still submitted from a previous adapter run
// are failed here: their message text was never persisted (CR-A8-2).
func New(opts Options) (*Executor, error) {
	if opts.Table == nil || opts.Driver == nil {
		return nil, errors.New("executor: table and driver are required")
	}
	if opts.TaskDeadline <= 0 {
		opts.TaskDeadline = DefaultTaskDeadline
	}
	if opts.TerminationTimeout <= 0 {
		opts.TerminationTimeout = DefaultTerminationTimeout
	}
	if opts.MaxResultBytes <= 0 {
		opts.MaxResultBytes = DefaultMaxResultBytes
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	procs, err := platformProcessOps()
	if err != nil {
		return nil, err
	}
	e := &Executor{
		opts:    opts,
		procs:   procs,
		texts:   map[string]string{},
		subs:    map[string][]*subscriber{},
		active:  map[int]bool{},
		pending: map[string]string{},
		wake:    make(chan struct{}, 1),
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, rec := range opts.Table.Tasks() {
		if rec.State == a2a.TaskStateSubmitted {
			e.finishQueuedLocked(rec, a2a.TaskStateFailed, a2a.ReasonOutcomeUnknown)
		}
	}
	return e, nil
}

// Submit hands the executor the message text of an admitted task; call it right after Admit.
func (e *Executor) Submit(taskID, text string) {
	e.mu.Lock()
	if rec, err := e.opts.Table.LookupTask(taskID); err == nil && rec.State == a2a.TaskStateSubmitted {
		e.texts[taskID] = text
	}
	e.mu.Unlock()
	e.notify()
}

func (e *Executor) notify() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Run implements a2aagent.Executor. Working tasks left by a previous adapter
// run are killed and confirmed before anything is dequeued (proposal 14).
func (e *Executor) Run(ctx context.Context) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return errors.New("executor: Run already called")
	}
	e.started = true
	e.mu.Unlock()

	e.recoverWorking()

	poll := time.NewTicker(e.opts.PollInterval)
	defer poll.Stop()
	var heartbeat <-chan time.Time
	if e.opts.HeartbeatInterval > 0 {
		hb := time.NewTicker(e.opts.HeartbeatInterval)
		defer hb.Stop()
		heartbeat = hb.C
		e.housekeep()
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		if r := e.dequeue(); r != nil {
			e.execute(ctx, r)
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-e.wake:
		case <-poll.C:
		case <-heartbeat:
			e.housekeep()
		}
	}
}

func (e *Executor) housekeep() {
	now := e.opts.Now()
	if err := e.opts.Table.Heartbeat(now); err != nil {
		e.logf("heartbeat: %v", err)
	}
	if err := e.opts.Table.Expire(now); err != nil {
		e.logf("expire: %v", err)
	}
}

func (e *Executor) recoverWorking() {
	for _, rec := range e.opts.Table.Tasks() {
		if rec.State != a2a.TaskStateWorking {
			continue
		}
		// PGID 0 means the crash fell between persisting "working" and starting; the process may exist.
		term := a2a.TerminationUnconfirmed
		if rec.PGID > 0 {
			term = e.terminateGroup(rec.PGID)
		}
		e.complete(rec, nil, a2a.ReasonOutcomeUnknown, term, "")
	}
}

func (e *Executor) dequeue() *run {
	e.mu.Lock()
	defer e.mu.Unlock()
	for {
		next, err := e.opts.Table.Next()
		if err != nil {
			e.logf("next: %v", err)
			return nil
		}
		if next == nil {
			return nil
		}
		if e.epoch != "" && next.Epoch != e.epoch {
			e.finishQueuedLocked(next, a2a.TaskStateFailed, a2a.ReasonEpochChanged)
			continue
		}
		text, ok := e.texts[next.TaskID]
		if !ok {
			return nil
		}
		delete(e.texts, next.TaskID)
		r := &run{
			taskID: next.TaskID, principal: next.Principal, epoch: next.Epoch, text: text,
			stop: make(chan struct{}), done: make(chan struct{}),
		}
		e.running = r
		return r
	}
}

type decodeResult struct {
	outcome a2aagent.Outcome
	err     error
}

func (e *Executor) execute(ctx context.Context, r *run) {
	rec, err := e.opts.Table.LookupTask(r.taskID)
	if err != nil {
		e.logf("task %s vanished before start", r.taskID)
		e.release(r, nil)
		return
	}
	now := e.opts.Now()
	rec.State = a2a.TaskStateWorking
	rec.StartedAt = formatTime(now)
	rec.DeadlineAt = formatTime(now.Add(e.opts.TaskDeadline))
	rec.PGID = 0
	if err := e.opts.Table.UpdateTask(rec); err != nil {
		e.logf("task %s: persist working: %v", r.taskID, err)
		e.complete(rec, r, "", a2a.TerminationNotApplicable, "")
		return
	}
	e.emitStatus(rec)
	deadline := time.NewTimer(e.opts.TaskDeadline)
	defer deadline.Stop()

	select {
	case <-r.stop:
		e.complete(rec, r, r.reason, a2a.TerminationNotApplicable, "")
		return
	default:
	}

	proc, err := e.start(rec, r.text)
	if err != nil {
		e.logf("task %s: start harness: %v", r.taskID, err)
		e.complete(rec, r, "", a2a.TerminationNotApplicable, "")
		return
	}
	rec.PGID = proc.pgid
	if err := e.opts.Table.UpdateTask(rec); err != nil {
		e.logf("task %s: persist pgid: %v", r.taskID, err)
	}

	var reason a2a.EndingReason
	var term a2a.Termination
	var waitErr error
	exited := false
	select {
	case waitErr = <-proc.exited:
		exited = true
		term = e.sweep()
	case <-r.stop:
		reason = r.reason
	case <-deadline.C:
		reason = a2a.ReasonDeadline
	case <-ctx.Done():
		reason = a2a.ReasonOutcomeUnknown
	}
	if !exited {
		term = e.terminate(proc.pgid)
	}

	var dec decodeResult
	select {
	case dec = <-proc.decoded:
	case <-time.After(e.opts.TerminationTimeout):
		_ = proc.stdout.Close()
		dec = <-proc.decoded
	}
	_ = proc.stdout.Close()
	if exited || term == a2a.TerminationConfirmed {
		<-proc.waited
	}
	e.untrack(proc.pgid)
	if dec.err != nil {
		e.logf("task %s: decode: %v", r.taskID, dec.err)
	}
	if waitErr != nil {
		e.logf("task %s: harness exit: %v", r.taskID, waitErr)
	}
	if dec.outcome.HarnessSessionID != "" || len(dec.outcome.Transcripts) > 0 {
		if err := e.opts.Table.BindHarnessSession(rec.ContextID, dec.outcome.HarnessSessionID, dec.outcome.Transcripts); err != nil {
			e.logf("task %s: bind session: %v", r.taskID, err)
		}
	}
	succeeded := exited && waitErr == nil && dec.err == nil
	if succeeded {
		rec.State = a2a.TaskStateCompleted
	}
	e.complete(rec, r, reason, term, dec.outcome.FinalMessage)
}

type harnessProc struct {
	pgid    int
	stdout  *os.File
	exited  <-chan error
	waited  <-chan struct{}
	decoded <-chan decodeResult
}

func (e *Executor) start(rec *a2aagent.TaskRecord, text string) (*harnessProc, error) {
	inv := a2aagent.Invocation{
		Prompt:  e.prompt(rec, text),
		WorkDir: e.opts.WorkDir,
		Env:     harnessEnv(e.opts.Env),
	}
	c, err := e.opts.Table.Context(rec.Principal, rec.ContextID)
	if err != nil {
		// Proposal 14: resume is refused when the binding is gone.
		return nil, fmt.Errorf("context binding: %w", errors.New(describe(err)))
	}
	inv.HarnessSessionID, inv.NewHarnessSessionID, err = e.sessionIDs(rec.ContextID, c.HarnessSessionID)
	if err != nil {
		return nil, err
	}
	cmd, err := e.opts.Driver.Command(context.Background(), inv)
	if err != nil {
		return nil, err
	}
	if inventory, ok := e.opts.Driver.(a2aagent.InvocationInventory); ok {
		if paths := inventory.InvocationTranscripts(inv); len(paths) > 0 {
			if err := e.opts.Table.BindHarnessSession(rec.ContextID, "", paths); err != nil {
				return nil, fmt.Errorf("persist harness inventory: %w", errors.New(describe(err)))
			}
		}
	}
	if cmd.Env == nil {
		cmd.Env = inv.Env
	} else {
		cmd.Env = harnessEnv(cmd.Env)
	}
	if cmd.Dir == "" {
		cmd.Dir = inv.WorkDir
	}
	setProcessGroup(cmd)

	// Own pipes keep Wait independent of descendants that hold stdio open.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		for _, f := range []*os.File{stdinR, stdinW, stdoutR, stdoutW} {
			_ = f.Close()
		}
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, stderrW
	e.mu.Lock()
	err = cmd.Start()
	if err == nil {
		e.active[cmd.Process.Pid] = true
	}
	e.mu.Unlock()
	for _, f := range []*os.File{stdinR, stdoutW, stderrW} {
		_ = f.Close()
	}
	if err != nil {
		for _, f := range []*os.File{stdinW, stdoutR, stderrR} {
			_ = f.Close()
		}
		return nil, err
	}

	go func() {
		_, _ = io.WriteString(stdinW, inv.Prompt)
		_ = stdinW.Close()
	}()
	go func() {
		_, _ = io.Copy(e.opts.Log, stderrR)
		_ = stderrR.Close()
	}()
	exited := make(chan error, 1)
	waited := make(chan struct{})
	go func() {
		exited <- cmd.Wait()
		close(waited)
	}()
	decoded := make(chan decodeResult, 1)
	contextID := rec.ContextID
	go func() {
		out, err := e.opts.Driver.Decode(stdoutR, func(ev a2aagent.HarnessEvent) {
			if ev.Kind == a2aagent.EventSession && ev.HarnessSessionID != "" {
				if err := e.opts.Table.BindHarnessSession(contextID, ev.HarnessSessionID, nil); err != nil {
					e.logf("bind session: %v", err)
				}
				e.mu.Lock()
				delete(e.pending, contextID)
				e.mu.Unlock()
			}
		})
		decoded <- decodeResult{outcome: out, err: err}
	}()
	return &harnessProc{pgid: cmd.Process.Pid, stdout: stdoutR, exited: exited, waited: waited, decoded: decoded}, nil
}

// sessionIDs returns the session to resume or the new one to create. A new ID is
// bound before spawn so a crash leaves a deletable binding; until the harness
// reports it, the next turn creates it again instead of resuming.
func (e *Executor) sessionIDs(contextID, bound string) (resume, create string, err error) {
	e.mu.Lock()
	pending := e.pending[contextID]
	e.mu.Unlock()
	switch {
	case bound != "" && bound == pending:
		return "", pending, nil
	case bound != "":
		return bound, "", nil
	}
	id, err := e.opts.Driver.NewSessionID()
	if err != nil || id == "" {
		return "", "", err
	}
	if err := e.opts.Table.BindHarnessSession(contextID, id, nil); err != nil {
		return "", "", fmt.Errorf("bind new harness session: %w", errors.New(describe(err)))
	}
	e.mu.Lock()
	e.pending[contextID] = id
	e.mu.Unlock()
	return "", id, nil
}

func (e *Executor) untrack(pgid int) {
	e.mu.Lock()
	delete(e.active, pgid)
	e.mu.Unlock()
}

func (e *Executor) prompt(rec *a2aagent.TaskRecord, text string) string {
	header := fmt.Sprintf(a2a.ProvenanceFormat, e.opts.Group, rec.Principal.Member, rec.Principal.Role,
		rec.Principal.Generation, rec.TaskID, rec.ContextID)
	return header + "\n\n" + text
}

func harnessEnv(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "ENCLAVE_A2A_") {
			out = append(out, kv)
		}
	}
	return out
}

// terminate ends a running harness (CR-P2-1): SIGTERM to its process group,
// then SIGKILL to every descendant of this process until the tree is empty.
func (e *Executor) terminate(pgid int) a2a.Termination {
	if err := e.procs.signalGroup(pgid, sigTerm); err != nil {
		e.logf("signal process group %d: %v", pgid, err)
	}
	if e.waitTreeEmpty(e.opts.TerminationTimeout, 0) {
		return a2a.TerminationConfirmed
	}
	return e.killTree()
}

// sweep runs after every harness exit, success included: leftovers such as
// a tool's setsid child get SIGTERM, then SIGKILL, and the tree must end empty.
func (e *Executor) sweep() a2a.Termination {
	if e.waitTreeEmpty(0, 0) {
		return a2a.TerminationConfirmed
	}
	e.logf("harness left descendants running; terminating them")
	if e.waitTreeEmpty(e.opts.TerminationTimeout, sigTerm) {
		return a2a.TerminationConfirmed
	}
	return e.killTree()
}

func (e *Executor) killTree() a2a.Termination {
	if e.waitTreeEmpty(e.opts.TerminationTimeout, sigKill) {
		return a2a.TerminationConfirmed
	}
	e.logf("descendants survived SIGKILL for %s", e.opts.TerminationTimeout)
	return a2a.TerminationUnconfirmed
}

// waitTreeEmpty polls the descendant tree for up to timeout. A non-zero sig
// goes to every live member seen, on each pass, so late forks are caught too.
func (e *Executor) waitTreeEmpty(timeout time.Duration, sig signal) bool {
	step := max(min(timeout/20, 50*time.Millisecond), time.Millisecond)
	end := time.Now().Add(timeout)
	termed := map[int]bool{}
	for {
		live, err := e.liveDescendants()
		if err == nil && len(live) == 0 {
			return true
		}
		for _, pid := range live {
			if sig == 0 || (sig == sigTerm && termed[pid]) {
				continue
			}
			termed[pid] = true
			if err := e.procs.signalProcess(pid, sig); err != nil {
				e.logf("signal descendant %d: %v", pid, err)
			}
		}
		if !time.Now().Before(end) {
			return false
		}
		time.Sleep(step)
	}
}

// liveDescendants lists this process's live descendants and reaps zombie
// children other than running harness leaders, which cmd.Wait collects.
func (e *Executor) liveDescendants() ([]int, error) {
	procs, err := e.procs.scan()
	if err != nil {
		return nil, err
	}
	e.reapChildren(procs)
	var live []int
	for _, p := range descendants(procs, e.procs.self) {
		if !p.Zombie {
			live = append(live, p.PID)
		}
	}
	return live, nil
}

func (e *Executor) reapChildren(procs []procInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range procs {
		if p.Zombie && p.PPID == e.procs.self && !e.active[p.PID] {
			e.procs.reap(p.PID)
		}
	}
}

// terminateGroup is the restart path: the surviving harness is outside this
// process's tree, so only its recorded process group can be signaled and checked.
func (e *Executor) terminateGroup(pgid int) a2a.Termination {
	for _, sig := range []signal{sigTerm, sigKill} {
		if err := e.procs.signalGroup(pgid, sig); err != nil {
			e.logf("signal process group %d: %v", pgid, err)
		}
		if e.waitGroupGone(pgid, e.opts.TerminationTimeout) {
			return a2a.TerminationConfirmed
		}
	}
	e.logf("process group %d survived SIGKILL for %s", pgid, e.opts.TerminationTimeout)
	return a2a.TerminationUnconfirmed
}

func (e *Executor) waitGroupGone(pgid int, timeout time.Duration) bool {
	step := max(min(timeout/20, 50*time.Millisecond), time.Millisecond)
	end := time.Now().Add(timeout)
	for {
		alive, err := e.groupAlive(pgid)
		if err == nil && !alive {
			return true
		}
		if !time.Now().Before(end) {
			return false
		}
		time.Sleep(step)
	}
}

func (e *Executor) groupAlive(pgid int) (bool, error) {
	procs, err := e.procs.scan()
	if err != nil {
		return true, err
	}
	e.reapChildren(procs)
	for _, p := range procs {
		if p.PGID == pgid && !p.Zombie {
			return true, nil
		}
	}
	return false, nil
}

func descendants(procs []procInfo, root int) []procInfo {
	children := map[int][]procInfo{}
	for _, p := range procs {
		children[p.PPID] = append(children[p.PPID], p)
	}
	var out []procInfo
	queue := []int{root}
	seen := map[int]bool{root: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, c := range children[pid] {
			if !seen[c.PID] {
				seen[c.PID] = true
				out = append(out, c)
				queue = append(queue, c.PID)
			}
		}
	}
	return out
}

// complete records the ending per proposal 12 and releases the run. An
// unconfirmed termination quarantines the worker before the task record says so.
func (e *Executor) complete(rec *a2aagent.TaskRecord, r *run, reason a2a.EndingReason, term a2a.Termination, result string) {
	if term == a2a.TerminationUnconfirmed {
		q := &a2aagent.Quarantine{TaskID: rec.TaskID, PGID: rec.PGID, Reason: QuarantineReason}
		if err := e.opts.Table.SetQuarantine(q); err != nil {
			e.logf("task %s: persist quarantine: %v", rec.TaskID, err)
		}
	}
	switch {
	case reason == a2a.ReasonCancel && term != a2a.TerminationUnconfirmed:
		rec.State = a2a.TaskStateCanceled
	case reason != "" || rec.State != a2a.TaskStateCompleted:
		rec.State = a2a.TaskStateFailed
	}
	rec.Reason = reason
	rec.Termination = term
	rec.EndedAt = formatTime(e.opts.Now())
	rec.Result, rec.Truncated = truncate(result, e.opts.MaxResultBytes)
	if err := e.opts.Table.UpdateTask(rec); err != nil {
		e.logf("task %s: persist ending: %v", rec.TaskID, err)
	}
	e.mu.Lock()
	e.emitTerminalLocked(rec)
	e.mu.Unlock()
	e.release(r, rec)
}

func (e *Executor) release(r *run, rec *a2aagent.TaskRecord) {
	if r == nil {
		return
	}
	e.mu.Lock()
	if rec != nil {
		r.final = copyTask(rec)
	}
	if e.running == r {
		e.running = nil
	}
	e.mu.Unlock()
	close(r.done)
	e.notify()
}

// finishQueuedLocked ends a task that never ran; termination is not applicable.
func (e *Executor) finishQueuedLocked(rec *a2aagent.TaskRecord, state a2a.TaskState, reason a2a.EndingReason) {
	rec.State = state
	rec.Reason = reason
	rec.Termination = a2a.TerminationNotApplicable
	rec.EndedAt = formatTime(e.opts.Now())
	delete(e.texts, rec.TaskID)
	if err := e.opts.Table.UpdateTask(rec); err != nil {
		e.logf("task %s: persist ending: %v", rec.TaskID, err)
	}
	e.emitTerminalLocked(rec)
}

// truncate keeps s within limit bytes, marker included, cutting on a rune boundary.
func truncate(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := max(limit-len(a2a.TruncatedMarker), 0)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + a2a.TruncatedMarker, true
}

// Cancel implements a2aagent.Executor. A queued task is canceled with
// termination not-applicable; a running one only when the kill is confirmed.
func (e *Executor) Cancel(ctx context.Context, taskID string) (*a2aagent.TaskRecord, error) {
	e.mu.Lock()
	if r := e.running; r != nil && r.taskID == taskID {
		e.mu.Unlock()
		return e.stopRun(ctx, r, a2a.ReasonCancel)
	}
	defer e.mu.Unlock()
	rec, err := e.opts.Table.LookupTask(taskID)
	if err != nil {
		return nil, err
	}
	if rec.State != a2a.TaskStateSubmitted {
		return rec, contexttable.Refusal(a2a.CodeTaskNotCancelable, "task already ended", contexttable.ReasonNotCancelable)
	}
	e.finishQueuedLocked(rec, a2a.TaskStateCanceled, a2a.ReasonCancel)
	return copyTask(rec), nil
}

func (e *Executor) stopRun(ctx context.Context, r *run, reason a2a.EndingReason) (*a2aagent.TaskRecord, error) {
	r.requestStop(reason)
	select {
	case <-r.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if r.final == nil {
		return e.opts.Table.LookupTask(r.taskID)
	}
	return copyTask(r.final), nil
}

// EpochChanged implements a2aagent.Executor.
func (e *Executor) EpochChanged(ctx context.Context, epoch string) error {
	e.mu.Lock()
	e.epoch = epoch
	for _, rec := range e.opts.Table.Tasks() {
		if rec.State == a2a.TaskStateSubmitted && rec.Epoch != epoch {
			e.finishQueuedLocked(rec, a2a.TaskStateFailed, a2a.ReasonEpochChanged)
		}
	}
	r := e.running
	e.mu.Unlock()
	if r != nil && r.epoch != epoch {
		_, err := e.stopRun(ctx, r, a2a.ReasonEpochChanged)
		return err
	}
	return nil
}

// HubLost ends running and queued work after the reconnect window elapsed
// (proposal 12 "hub-lost", CR-A8-1).
func (e *Executor) HubLost(ctx context.Context) error {
	e.mu.Lock()
	for _, rec := range e.opts.Table.Tasks() {
		if rec.State == a2a.TaskStateSubmitted {
			e.finishQueuedLocked(rec, a2a.TaskStateFailed, a2a.ReasonHubLost)
		}
	}
	r := e.running
	e.mu.Unlock()
	if r != nil {
		_, err := e.stopRun(ctx, r, a2a.ReasonHubLost)
		return err
	}
	return nil
}

func (e *Executor) discardQueuedLocked(principal a2aagent.PrincipalRef) []string {
	discarded := []string{}
	for _, rec := range e.opts.Table.Tasks() {
		if rec.State == a2a.TaskStateSubmitted && rec.Principal == principal {
			e.finishQueuedLocked(rec, a2a.TaskStateFailed, a2a.ReasonOrchestratorLost)
			discarded = append(discarded, rec.TaskID)
		}
	}
	return discarded
}

// Depart implements a2aagent.Executor: persist the admission stop and discard
// the principal's queued work. Only an authoritative departure terminates its
// running work; a suspected one lets it run to its deadline (proposal 13).
func (e *Executor) Depart(ctx context.Context, principal a2aagent.PrincipalRef, authoritative bool) (a2a.DepartureAck, error) {
	ack := a2a.DepartureAck{
		Worker: e.opts.Worker, WorkerGeneration: e.opts.WorkerGeneration,
		Principal: a2a.Principal{
			Group: e.opts.Group, GroupGeneration: e.opts.GroupGeneration,
			Member: principal.Member, Role: principal.Role, Generation: principal.Generation,
		},
		RunningTerminated: []string{},
		Termination:       a2a.TerminationNotApplicable,
	}
	reason := contexttable.StopSuspected
	if authoritative {
		reason = contexttable.StopAuthoritative
	}
	if err := e.opts.Table.StopAdmission(principal, reason); err != nil {
		return ack, err
	}
	ack.AdmissionStopPersisted = true

	e.mu.Lock()
	ack.QueuedDiscarded = e.discardQueuedLocked(principal)
	r := e.running
	e.mu.Unlock()
	if authoritative && r != nil && r.principal == principal {
		final, err := e.stopRun(ctx, r, a2a.ReasonOrchestratorLost)
		if err != nil {
			return ack, err
		}
		ack.Termination = final.Termination
		if final.Reason == a2a.ReasonOrchestratorLost {
			ack.RunningTerminated = append(ack.RunningTerminated, final.TaskID)
		}
	}
	ack.At = formatTime(e.opts.Now())
	return ack, nil
}

// DescendantsAlive implements a2aagent.Executor: the subreaper tree is
// non-empty (CR-P2-1), or the quarantined process group still has members,
// which is the only check left after an adapter restart.
func (e *Executor) DescendantsAlive() (bool, error) {
	restr, err := e.opts.Table.Restrictions()
	if err != nil {
		return true, err
	}
	live, err := e.liveDescendants()
	if err != nil {
		return true, err
	}
	if len(live) > 0 {
		return true, nil
	}
	if q := restr.Quarantine; q != nil && q.PGID > 0 {
		return e.groupAlive(q.PGID)
	}
	return false, nil
}

// Subscribe implements a2aagent.Executor. Dropping a subscription never cancels.
func (e *Executor) Subscribe(taskID string) (<-chan a2aagent.TaskEvent, func()) {
	sub := &subscriber{ch: make(chan a2aagent.TaskEvent, 64)}
	e.mu.Lock()
	defer e.mu.Unlock()
	rec, err := e.opts.Table.LookupTask(taskID)
	switch {
	case err != nil:
		close(sub.ch)
		sub.closed = true
	case rec.State != a2a.TaskStateSubmitted && rec.State != a2a.TaskStateWorking:
		for _, ev := range terminalEvents(rec) {
			sub.ch <- ev
		}
		close(sub.ch)
		sub.closed = true
	default:
		e.subs[taskID] = append(e.subs[taskID], sub)
	}
	return sub.ch, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if sub.closed {
			return
		}
		sub.closed = true
		close(sub.ch)
		list := e.subs[taskID]
		for i, s := range list {
			if s == sub {
				e.subs[taskID] = append(list[:i], list[i+1:]...)
				break
			}
		}
	}
}

func (e *Executor) emitStatus(rec *a2aagent.TaskRecord) {
	ev := a2aagent.TaskEvent{Task: copyTask(rec), Event: a2a.Result{StatusUpdate: &a2a.TaskStatusUpdateEvent{
		TaskID: rec.TaskID, ContextID: rec.ContextID,
		Status: a2a.TaskStatus{State: rec.State, Timestamp: formatTime(e.opts.Now())},
	}}}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.subs[rec.TaskID] {
		select {
		case s.ch <- ev:
		default:
		}
	}
}

func (e *Executor) emitTerminalLocked(rec *a2aagent.TaskRecord) {
	events := terminalEvents(rec)
	for _, s := range e.subs[rec.TaskID] {
		for _, ev := range events {
			// Terminal events must arrive; make room by dropping a stale progress event.
			select {
			case s.ch <- ev:
			default:
				select {
				case <-s.ch:
				default:
				}
				s.ch <- ev
			}
		}
		s.closed = true
		close(s.ch)
	}
	delete(e.subs, rec.TaskID)
}

func terminalEvents(rec *a2aagent.TaskRecord) []a2aagent.TaskEvent {
	var out []a2aagent.TaskEvent
	if a2aagent.HasResultArtifact(rec) {
		text := rec.Result
		out = append(out, a2aagent.TaskEvent{Task: copyTask(rec), Event: a2a.Result{ArtifactUpdate: &a2a.TaskArtifactUpdateEvent{
			TaskID: rec.TaskID, ContextID: rec.ContextID, LastChunk: true,
			Artifact: a2a.Artifact{ArtifactID: ResultArtifactID, Parts: []a2a.Part{{Text: &text}}},
		}}})
	}
	meta := map[string]any{a2a.ExtensionURI: a2aagent.TerminalMeta(rec)}
	out = append(out, a2aagent.TaskEvent{Task: copyTask(rec), Event: a2a.Result{StatusUpdate: &a2a.TaskStatusUpdateEvent{
		TaskID: rec.TaskID, ContextID: rec.ContextID, Metadata: meta,
		Status: a2a.TaskStatus{State: rec.State, Timestamp: rec.EndedAt},
	}}})
	return out
}

func (e *Executor) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(e.opts.Log, "a2a executor: "+format+"\n", args...)
}

// describe formats err without calling a2a.Error.Error, a stub until A1 lands.
func describe(err error) string {
	var ae *a2a.Error
	if errors.As(err, &ae) {
		return fmt.Sprintf("a2a error %d: %s", ae.Code, ae.Message)
	}
	return err.Error()
}

func copyTask(rec *a2aagent.TaskRecord) *a2aagent.TaskRecord {
	cp := *rec
	return &cp
}

func formatTime(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}
