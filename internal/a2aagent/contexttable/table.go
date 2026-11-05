// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package contexttable is the worker adapter's persisted context table
// (interfaces.md section 5), implementing a2aagent.ContextTable.
package contexttable

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
	"enclave/internal/util"
)

// Admission stop reasons persisted in restrictions.json.
const (
	StopSuspected     = "suspected"
	StopAuthoritative = "authoritative"
)

// Defaults from proposal 17, used when an Options bound is zero.
const (
	DefaultQueueDepth   = 8
	DefaultRecordLimit  = 1000
	DefaultRecordMaxAge = 7 * 24 * time.Hour
	DefaultContextTTL   = 24 * time.Hour
)

const fileMode = 0o600

// Options configures a Table. Dir is the member-generation directory of
// interfaces.md section 5; the table makes no assumption about where it lives.
type Options struct {
	Dir string
	// Header identifies the owner; SchemaVersion and UpdatedAt are set by the table.
	Header       a2aagent.TableHeader
	QueueDepth   int
	RecordLimit  int
	RecordMaxAge time.Duration
	ContextTTL   time.Duration
	// TranscriptRoot resolves config-store-relative transcript paths; empty skips deletion.
	TranscriptRoot string
	Now            func() time.Time
}

// Table implements a2aagent.ContextTable over the files in Options.Dir.
type Table struct {
	opts Options

	mu       sync.Mutex
	contexts a2aagent.ContextsFileData
	tasks    map[string]*a2aagent.TaskRecord
	dedupe   a2aagent.DedupeFileData
	restr    a2aagent.RestrictionsFileData
	seq      uint64
}

var _ a2aagent.ContextTable = (*Table)(nil)

// Open loads the table from opts.Dir, creating it when absent, and repairs
// records a crash left between the task file and the dedupe or context write.
func Open(opts Options) (*Table, error) {
	if opts.Dir == "" {
		return nil, errors.New("context table: directory is required")
	}
	if opts.Header.Worker == "" || opts.Header.WorkerGeneration == "" {
		return nil, errors.New("context table: worker and worker generation are required")
	}
	if opts.QueueDepth <= 0 {
		opts.QueueDepth = DefaultQueueDepth
	}
	if opts.RecordLimit <= 0 {
		opts.RecordLimit = DefaultRecordLimit
	}
	if opts.RecordMaxAge <= 0 {
		opts.RecordMaxAge = DefaultRecordMaxAge
	}
	if opts.ContextTTL <= 0 {
		opts.ContextTTL = DefaultContextTTL
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	opts.Header.SchemaVersion = a2aagent.SchemaVersion
	for _, dir := range []string{opts.Dir, filepath.Join(opts.Dir, a2aagent.TasksDirName), filepath.Join(opts.Dir, a2aagent.TranscriptsDir)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("context table: %w", err)
		}
	}
	t := &Table{opts: opts, tasks: map[string]*a2aagent.TaskRecord{}}
	if err := t.load(); err != nil {
		return nil, fmt.Errorf("context table: %w", err)
	}
	return t, nil
}

func (t *Table) load() error {
	if _, err := t.readFile(a2aagent.ContextsFile, &t.contexts, &t.contexts.TableHeader); err != nil {
		return err
	}
	if _, err := t.readFile(a2aagent.DedupeFile, &t.dedupe, &t.dedupe.TableHeader); err != nil {
		return err
	}
	if _, err := t.readFile(a2aagent.RestrictionsFile, &t.restr, &t.restr.TableHeader); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(t.opts.Dir, a2aagent.TasksDirName))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		rec := &a2aagent.TaskRecord{}
		if _, err := t.readFile(filepath.Join(a2aagent.TasksDirName, name), rec, &rec.TableHeader); err != nil {
			return err
		}
		if rec.TaskID+".json" != name {
			return fmt.Errorf("%s: task ID %q does not match the file name", name, rec.TaskID)
		}
		t.tasks[rec.TaskID] = rec
		if rec.Seq > t.seq {
			t.seq = rec.Seq
		}
	}

	now := t.opts.Now()
	dedupeRepaired, contextsRepaired := false, false
	for _, rec := range t.sortedTasks() {
		if t.findDedupe(rec.Principal, rec.MessageID) == nil {
			t.dedupe.Records = append(t.dedupe.Records, a2aagent.DedupeRecord{
				Principal: rec.Principal, MessageID: rec.MessageID, ContentHash: rec.ContentHash,
				TaskID: rec.TaskID, AdmittedAt: rec.AdmittedAt,
			})
			dedupeRepaired = true
		}
		if t.findContext(rec.ContextID) == nil && t.findTombstone(rec.ContextID) == nil {
			t.contexts.Contexts = append(t.contexts.Contexts, a2aagent.ContextRecord{
				ContextID: rec.ContextID, Principal: rec.Principal,
				CreatedAt: rec.AdmittedAt, LastActivityAt: rec.AdmittedAt, Transcripts: []string{},
			})
			contextsRepaired = true
		}
	}
	if dedupeRepaired {
		if err := t.writeDedupe(now); err != nil {
			return err
		}
	}
	if contextsRepaired || t.contexts.SchemaVersion == "" {
		if err := t.writeContexts(now); err != nil {
			return err
		}
	}
	if t.dedupe.SchemaVersion == "" {
		if err := t.writeDedupe(now); err != nil {
			return err
		}
	}
	return t.writeRestrictions(now)
}

// readFile decodes rel into v and checks the header; a missing file leaves v zero.
func (t *Table) readFile(rel string, v any, header *a2aagent.TableHeader) (bool, error) {
	data, err := os.ReadFile(filepath.Join(t.opts.Dir, rel)) // #nosec G304 -- table directory is supplied by the adapter.
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("%s: %w", rel, err)
	}
	if header.SchemaVersion != a2aagent.SchemaVersion {
		return false, fmt.Errorf("%s: unsupported schemaVersion %q", rel, header.SchemaVersion)
	}
	if header.Worker != t.opts.Header.Worker || header.WorkerGeneration != t.opts.Header.WorkerGeneration {
		return false, fmt.Errorf("%s: owned by %s/%s, not %s/%s", rel, header.Worker, header.WorkerGeneration,
			t.opts.Header.Worker, t.opts.Header.WorkerGeneration)
	}
	return true, nil
}

func (t *Table) header(now time.Time) a2aagent.TableHeader {
	h := t.opts.Header
	h.UpdatedAt = formatTime(now)
	return h
}

func (t *Table) writeJSON(rel string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(t.opts.Dir, rel)
	if err := util.WriteFileAtomic(path, append(data, '\n'), fileMode); err != nil {
		return err
	}
	return util.SyncDir(filepath.Dir(path))
}

func (t *Table) writeContexts(now time.Time) error {
	t.contexts.TableHeader = t.header(now)
	if t.contexts.Contexts == nil {
		t.contexts.Contexts = []a2aagent.ContextRecord{}
	}
	if t.contexts.Tombstones == nil {
		t.contexts.Tombstones = []a2aagent.Tombstone{}
	}
	return t.writeJSON(a2aagent.ContextsFile, &t.contexts)
}

func (t *Table) writeDedupe(now time.Time) error {
	t.dedupe.TableHeader = t.header(now)
	if t.dedupe.Records == nil {
		t.dedupe.Records = []a2aagent.DedupeRecord{}
	}
	return t.writeJSON(a2aagent.DedupeFile, &t.dedupe)
}

func (t *Table) writeRestrictions(now time.Time) error {
	t.restr.TableHeader = t.header(now)
	if t.restr.AdmissionStops == nil {
		t.restr.AdmissionStops = []a2aagent.AdmissionStop{}
	}
	return t.writeJSON(a2aagent.RestrictionsFile, &t.restr)
}

func (t *Table) writeTask(rec *a2aagent.TaskRecord, now time.Time) error {
	rec.TableHeader = t.header(now)
	return t.writeJSON(filepath.Join(a2aagent.TasksDirName, rec.TaskID+".json"), rec)
}

func (t *Table) removeTask(taskID string) error {
	err := os.Remove(filepath.Join(t.opts.Dir, a2aagent.TasksDirName, taskID+".json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	delete(t.tasks, taskID)
	return nil
}

// Admit implements a2aagent.ContextTable. The task file is the commit point:
// Open rebuilds a dedupe or context record that a crash left unwritten.
func (t *Table) Admit(ctx context.Context, req a2aagent.AdmitRequest) (a2aagent.Admission, error) {
	if err := ctx.Err(); err != nil {
		return a2aagent.Admission{}, err
	}
	if req.Principal.Member == "" || req.Principal.Generation == "" || req.Principal.Role == "" ||
		req.MessageID == "" || !strings.HasPrefix(req.ContentHash, "sha256:") {
		return a2aagent.Admission{}, Refusal(a2a.CodeInvalidParams, "incomplete admission request", ReasonInvalidRequest)
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if d := t.findDedupe(req.Principal, req.MessageID); d != nil {
		if d.ContentHash != req.ContentHash {
			return a2aagent.Admission{}, Refusal(a2a.CodeMessageIDConflict,
				"message ID reused with different content", ReasonMessageIDConflict)
		}
		if !req.Retry {
			return a2aagent.Admission{}, Refusal(a2a.CodeMessageIDConflict,
				"message ID reused without the retry flag", ReasonMessageIDReused)
		}
		rec := t.tasks[d.TaskID]
		if rec == nil {
			return a2aagent.Admission{}, Refusal(a2a.CodeRetryAmbiguous,
				"original task record evicted", ReasonRetryAmbiguous)
		}
		return a2aagent.Admission{Task: copyTask(rec), Existing: true, QueuePosition: t.queuePosition(rec)}, nil
	}
	if req.Retry {
		return a2aagent.Admission{}, Refusal(a2a.CodeRetryAmbiguous,
			"original unknown, evicted or from another generation", ReasonRetryAmbiguous)
	}
	if t.restr.Quarantine != nil {
		return a2aagent.Admission{}, Refusal(a2a.CodeQuarantined, "worker quarantined", ReasonQuarantined)
	}
	if t.findStop(req.Principal) >= 0 {
		return a2aagent.Admission{}, Refusal(a2a.CodeAdmissionStopped,
			"admission stopped for this principal", ReasonAdmissionStopped)
	}

	now := t.opts.Now()
	contextID := req.ContextID
	newContext := contextID == ""
	if newContext {
		id, err := t.newID(func(id string) bool { return t.findContext(id) != nil || t.findTombstone(id) != nil })
		if err != nil {
			return a2aagent.Admission{}, err
		}
		contextID = id
	} else if c := t.findContext(contextID); c == nil || !samePrincipal(c.Principal, req.Principal) {
		if ts := t.findTombstone(contextID); ts != nil && samePrincipal(ts.Principal, req.Principal) {
			return a2aagent.Admission{}, errContextExpired()
		}
		return a2aagent.Admission{}, errContextUnknown()
	}

	if t.countState(a2a.TaskStateSubmitted) >= t.opts.QueueDepth {
		return a2aagent.Admission{}, Refusal(a2a.CodeQueueFull, "queue full", ReasonQueueFull)
	}
	if len(t.tasks) >= t.opts.RecordLimit {
		if err := t.pruneTasks(now, t.opts.RecordLimit-1); err != nil {
			return a2aagent.Admission{}, err
		}
		if len(t.tasks) >= t.opts.RecordLimit {
			return a2aagent.Admission{}, Refusal(a2a.CodeQueueFull, "task record limit reached", ReasonQueueFull)
		}
	}

	taskID, err := t.newID(func(id string) bool { return t.tasks[id] != nil })
	if err != nil {
		return a2aagent.Admission{}, err
	}
	stamp := formatTime(now)
	rec := &a2aagent.TaskRecord{
		TaskID: taskID, ContextID: contextID, Principal: req.Principal,
		MessageID: req.MessageID, ContentHash: req.ContentHash, Seq: t.seq + 1,
		Epoch: req.Epoch, State: a2a.TaskStateSubmitted, AdmittedAt: stamp,
	}
	if err := t.writeTask(rec, now); err != nil {
		return a2aagent.Admission{}, fmt.Errorf("context table: persist task: %w", err)
	}
	t.seq = rec.Seq
	t.tasks[taskID] = rec

	// A failed write after the task file withdraws the task so it never runs unacknowledged.
	rollback := func(err error) (a2aagent.Admission, error) {
		t.dedupe.Records = removeDedupe(t.dedupe.Records, taskID)
		if newContext {
			t.contexts.Contexts = removeContext(t.contexts.Contexts, contextID)
		}
		return a2aagent.Admission{}, errors.Join(fmt.Errorf("context table: persist admission: %w", err), t.removeTask(taskID))
	}
	t.dedupe.Records = append(t.dedupe.Records, a2aagent.DedupeRecord{
		Principal: req.Principal, MessageID: req.MessageID, ContentHash: req.ContentHash,
		TaskID: taskID, AdmittedAt: stamp,
	})
	if err := t.writeDedupe(now); err != nil {
		return rollback(err)
	}
	if newContext {
		t.contexts.Contexts = append(t.contexts.Contexts, a2aagent.ContextRecord{
			ContextID: contextID, Principal: req.Principal, CreatedAt: stamp,
			LastActivityAt: stamp, Transcripts: []string{},
		})
	} else {
		t.findContext(contextID).LastActivityAt = stamp
	}
	if err := t.writeContexts(now); err != nil {
		return rollback(err)
	}
	return a2aagent.Admission{Task: copyTask(rec), QueuePosition: t.queuePosition(rec)}, nil
}

// Task implements a2aagent.ContextTable.
func (t *Table) Task(principal a2aagent.PrincipalRef, taskID string) (*a2aagent.TaskRecord, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec := t.tasks[taskID]
	if rec == nil || !samePrincipal(rec.Principal, principal) {
		return nil, errTaskNotFound()
	}
	return copyTask(rec), nil
}

// LookupTask returns a task regardless of its binding; for the executor, never for client requests.
func (t *Table) LookupTask(taskID string) (*a2aagent.TaskRecord, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec := t.tasks[taskID]
	if rec == nil {
		return nil, errTaskNotFound()
	}
	return copyTask(rec), nil
}

// Tasks returns every task record in admission order.
func (t *Table) Tasks() []*a2aagent.TaskRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.sortedTasks()
	for i, rec := range out {
		out[i] = copyTask(rec)
	}
	return out
}

// UpdateTask implements a2aagent.ContextTable; identity and binding fields cannot change.
func (t *Table) UpdateTask(rec *a2aagent.TaskRecord) error {
	if rec == nil {
		return errors.New("context table: nil task record")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	cur := t.tasks[rec.TaskID]
	if cur == nil {
		return errTaskNotFound()
	}
	if cur.ContextID != rec.ContextID || !samePrincipal(cur.Principal, rec.Principal) ||
		cur.MessageID != rec.MessageID || cur.ContentHash != rec.ContentHash || cur.Seq != rec.Seq {
		return fmt.Errorf("context table: task %s: binding fields are immutable", rec.TaskID)
	}
	next := copyTask(rec)
	if err := t.writeTask(next, t.opts.Now()); err != nil {
		return fmt.Errorf("context table: persist task: %w", err)
	}
	t.tasks[rec.TaskID] = next
	return nil
}

// Next implements a2aagent.ContextTable; it returns nil while the worker is quarantined.
func (t *Table) Next() (*a2aagent.TaskRecord, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.restr.Quarantine != nil {
		return nil, nil
	}
	for _, rec := range t.sortedTasks() {
		if rec.State == a2a.TaskStateSubmitted && t.findStop(rec.Principal) < 0 {
			return copyTask(rec), nil
		}
	}
	return nil, nil
}

// BindHarnessSession implements a2aagent.ContextTable.
func (t *Table) BindHarnessSession(contextID, harnessSessionID string, transcripts []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.findContext(contextID)
	if c == nil {
		return errContextUnknown()
	}
	changed := false
	if harnessSessionID != "" && c.HarnessSessionID != harnessSessionID {
		c.HarnessSessionID = harnessSessionID
		changed = true
	}
	for _, p := range transcripts {
		if p != "" && !contains(c.Transcripts, p) {
			c.Transcripts = append(c.Transcripts, p)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return t.writeContexts(t.opts.Now())
}

// Context implements a2aagent.ContextTable; "expired" is answered only to the binding's principal.
func (t *Table) Context(principal a2aagent.PrincipalRef, contextID string) (*a2aagent.ContextRecord, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c := t.findContext(contextID); c != nil && samePrincipal(c.Principal, principal) {
		cp := *c
		cp.Transcripts = append([]string{}, c.Transcripts...)
		return &cp, nil
	}
	if ts := t.findTombstone(contextID); ts != nil && samePrincipal(ts.Principal, principal) {
		return nil, errContextExpired()
	}
	return nil, errContextUnknown()
}

// StopAdmission implements a2aagent.ContextTable; an authoritative stop is never downgraded.
func (t *Table) StopAdmission(principal a2aagent.PrincipalRef, reason string) error {
	if reason != StopSuspected && reason != StopAuthoritative {
		return fmt.Errorf("context table: invalid admission stop reason %q", reason)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.opts.Now()
	if i := t.findStop(principal); i >= 0 {
		if t.restr.AdmissionStops[i].Reason == reason || reason == StopSuspected {
			return nil
		}
		t.restr.AdmissionStops[i].Reason = reason
	} else {
		t.restr.AdmissionStops = append(t.restr.AdmissionStops, a2aagent.AdmissionStop{
			Principal: principal, Reason: reason, Since: formatTime(now),
		})
	}
	return t.writeRestrictions(now)
}

// ReleaseAdmission implements a2aagent.ContextTable.
func (t *Table) ReleaseAdmission(principal a2aagent.PrincipalRef) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	i := t.findStop(principal)
	if i < 0 {
		return nil
	}
	t.restr.AdmissionStops = append(t.restr.AdmissionStops[:i], t.restr.AdmissionStops[i+1:]...)
	return t.writeRestrictions(t.opts.Now())
}

// SetQuarantine implements a2aagent.ContextTable; nil clears it. An existing
// quarantine is kept, so the first unconfirmed termination stays on record.
func (t *Table) SetQuarantine(q *a2aagent.Quarantine) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if q != nil && t.restr.Quarantine != nil {
		return nil
	}
	if q != nil {
		cp := *q
		if cp.Since == "" {
			cp.Since = formatTime(t.opts.Now())
		}
		q = &cp
	}
	t.restr.Quarantine = q
	return t.writeRestrictions(t.opts.Now())
}

// Restrictions implements a2aagent.ContextTable.
func (t *Table) Restrictions() (a2aagent.RestrictionsFileData, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.restr
	out.AdmissionStops = append([]a2aagent.AdmissionStop{}, t.restr.AdmissionStops...)
	if t.restr.Quarantine != nil {
		q := *t.restr.Quarantine
		out.Quarantine = &q
	}
	return out, nil
}

// Heartbeat implements a2aagent.ContextTable.
func (t *Table) Heartbeat(now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.writeRestrictions(now)
}

// Expire implements a2aagent.ContextTable. Admission stops and quarantine never expire.
func (t *Table) Expire(now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	active := map[string]bool{}
	for _, rec := range t.tasks {
		if !isTerminal(rec.State) {
			active[rec.ContextID] = true
		}
	}
	contextsChanged := false
	kept := t.contexts.Contexts[:0]
	var expired []a2aagent.ContextRecord
	for _, c := range t.contexts.Contexts {
		last, err := parseTime(c.LastActivityAt)
		if !active[c.ContextID] && (err != nil || now.Sub(last) >= t.opts.ContextTTL) {
			expired = append(expired, c)
			continue
		}
		kept = append(kept, c)
	}
	t.contexts.Contexts = kept
	for _, c := range expired {
		t.contexts.Tombstones = append(t.contexts.Tombstones, a2aagent.Tombstone{
			ContextID: c.ContextID, Principal: c.Principal, ExpiredAt: formatTime(now),
		})
		contextsChanged = true
	}
	tombstones := pruneByAge(t.contexts.Tombstones, now, t.opts.RecordMaxAge, t.opts.RecordLimit,
		func(ts a2aagent.Tombstone) string { return ts.ExpiredAt }, func(a2aagent.Tombstone) bool { return true })
	if len(tombstones) != len(t.contexts.Tombstones) {
		contextsChanged = true
	}
	t.contexts.Tombstones = tombstones
	if contextsChanged {
		if err := t.writeContexts(now); err != nil {
			return err
		}
	}

	var errs []error
	for _, c := range expired {
		errs = append(errs, t.deleteTranscripts(c.Transcripts))
	}

	tasksBefore := len(t.tasks)
	if err := t.pruneTasks(now, t.opts.RecordLimit); err != nil {
		errs = append(errs, err)
	}
	records := pruneByAge(t.dedupe.Records, now, t.opts.RecordMaxAge, t.opts.RecordLimit,
		func(d a2aagent.DedupeRecord) string { return d.AdmittedAt },
		func(d a2aagent.DedupeRecord) bool {
			rec := t.tasks[d.TaskID]
			return rec == nil || isTerminal(rec.State)
		})
	if len(records) != len(t.dedupe.Records) || len(t.tasks) != tasksBefore {
		t.dedupe.Records = records
		if err := t.writeDedupe(now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// pruneTasks evicts terminal task records past the age bound, then the
// oldest terminal ones above limit. Active tasks are never evicted.
func (t *Table) pruneTasks(now time.Time, limit int) error {
	sorted := t.sortedTasks()
	keep := pruneByAge(sorted, now, t.opts.RecordMaxAge, limit,
		func(rec *a2aagent.TaskRecord) string {
			if rec.EndedAt != "" {
				return rec.EndedAt
			}
			return rec.AdmittedAt
		},
		func(rec *a2aagent.TaskRecord) bool { return isTerminal(rec.State) })
	kept := map[string]bool{}
	for _, rec := range keep {
		kept[rec.TaskID] = true
	}
	var errs []error
	for _, rec := range sorted {
		if !kept[rec.TaskID] {
			errs = append(errs, t.removeTask(rec.TaskID))
		}
	}
	return errors.Join(errs...)
}

// pruneByAge drops evictable items older than maxAge, then the oldest
// evictable ones while more than limit remain. items must be oldest first.
func pruneByAge[T any](items []T, now time.Time, maxAge time.Duration, limit int, stamp func(T) string, evictable func(T) bool) []T {
	out := make([]T, 0, len(items))
	for _, it := range items {
		ts, err := parseTime(stamp(it))
		if evictable(it) && (err != nil || now.Sub(ts) > maxAge) {
			continue
		}
		out = append(out, it)
	}
	excess := len(out) - limit
	if excess <= 0 {
		return out
	}
	final := out[:0]
	for _, it := range out {
		if excess > 0 && evictable(it) {
			excess--
			continue
		}
		final = append(final, it)
	}
	return final
}

func (t *Table) deleteTranscripts(paths []string) error {
	if t.opts.TranscriptRoot == "" {
		return nil
	}
	var errs []error
	for _, rel := range paths {
		if !filepath.IsLocal(rel) {
			errs = append(errs, fmt.Errorf("context table: refusing transcript path %q outside the config store", rel))
			continue
		}
		errs = append(errs, os.RemoveAll(filepath.Join(t.opts.TranscriptRoot, rel)))
	}
	return errors.Join(errs...)
}

func (t *Table) sortedTasks() []*a2aagent.TaskRecord {
	out := make([]*a2aagent.TaskRecord, 0, len(t.tasks))
	for _, rec := range t.tasks {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// queuePosition counts the tasks ahead, the running one included, so 0 means it starts now.
func (t *Table) queuePosition(rec *a2aagent.TaskRecord) int {
	if rec.State != a2a.TaskStateSubmitted {
		return 0
	}
	pos := 0
	for _, other := range t.tasks {
		if other.State == a2a.TaskStateWorking || (other.State == a2a.TaskStateSubmitted && other.Seq < rec.Seq) {
			pos++
		}
	}
	return pos
}

func (t *Table) countState(state a2a.TaskState) int {
	n := 0
	for _, rec := range t.tasks {
		if rec.State == state {
			n++
		}
	}
	return n
}

func (t *Table) findDedupe(principal a2aagent.PrincipalRef, messageID string) *a2aagent.DedupeRecord {
	for i := range t.dedupe.Records {
		d := &t.dedupe.Records[i]
		if d.MessageID == messageID && d.Principal.Member == principal.Member && d.Principal.Generation == principal.Generation {
			return d
		}
	}
	return nil
}

func (t *Table) findContext(id string) *a2aagent.ContextRecord {
	for i := range t.contexts.Contexts {
		if t.contexts.Contexts[i].ContextID == id {
			return &t.contexts.Contexts[i]
		}
	}
	return nil
}

func (t *Table) findTombstone(id string) *a2aagent.Tombstone {
	for i := range t.contexts.Tombstones {
		if t.contexts.Tombstones[i].ContextID == id {
			return &t.contexts.Tombstones[i]
		}
	}
	return nil
}

func (t *Table) findStop(principal a2aagent.PrincipalRef) int {
	for i, s := range t.restr.AdmissionStops {
		if samePrincipal(s.Principal, principal) {
			return i
		}
	}
	return -1
}

func (t *Table) newID(taken func(string) bool) (string, error) {
	for {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		if id := hex.EncodeToString(b[:]); !taken(id) {
			return id, nil
		}
	}
}

func samePrincipal(a, b a2aagent.PrincipalRef) bool {
	return a.Member == b.Member && a.Generation == b.Generation && a.Role == b.Role
}

func isTerminal(state a2a.TaskState) bool {
	return state != a2a.TaskStateSubmitted && state != a2a.TaskStateWorking
}

func copyTask(rec *a2aagent.TaskRecord) *a2aagent.TaskRecord {
	cp := *rec
	return &cp
}

func removeDedupe(records []a2aagent.DedupeRecord, taskID string) []a2aagent.DedupeRecord {
	out := records[:0]
	for _, d := range records {
		if d.TaskID != taskID {
			out = append(out, d)
		}
	}
	return out
}

func removeContext(contexts []a2aagent.ContextRecord, contextID string) []a2aagent.ContextRecord {
	out := contexts[:0]
	for _, c := range contexts {
		if c.ContextID != contextID {
			out = append(out, c)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func formatTime(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}
