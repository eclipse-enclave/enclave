// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package contexttable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enclave/internal/a2a"
	"enclave/internal/a2aagent"
)

var (
	orch    = a2aagent.PrincipalRef{Member: "orch", Role: a2a.RoleOrchestrator, Generation: "0000000000000001"}
	orchNew = a2aagent.PrincipalRef{Member: "orch", Role: a2a.RoleOrchestrator, Generation: "0000000000000002"}
	host    = a2aagent.PrincipalRef{Member: "_host", Role: a2a.RoleHost, Generation: "00000000000000aa"}
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newClock() *clock { return &clock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)} }

func testOptions(dir string, c *clock) Options {
	return Options{
		Dir: dir,
		Header: a2aagent.TableHeader{
			Group: "review", GroupGeneration: "00000000000000ff",
			Worker: "worker-1", WorkerGeneration: "0000000000000abc",
		},
		Now: c.Now,
	}
}

func openTable(t *testing.T, opts Options) *Table {
	t.Helper()
	tbl, err := Open(opts)
	if err != nil {
		t.Fatalf("Open: %s", describe(err))
	}
	return tbl
}

// describe formats an error without calling a2a.Error.Error, which is a stub until A1 lands.
func describe(err error) string {
	var ae *a2a.Error
	if errors.As(err, &ae) {
		return fmt.Sprintf("a2a error %d (%s)", ae.Code, ae.Message)
	}
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func wantCode(t *testing.T, err error, code int) {
	t.Helper()
	var ae *a2a.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("want a2a error %d, got %s", code, describe(err))
	}
	var info []a2a.ErrorInfo
	if err := json.Unmarshal(ae.Data, &info); err != nil || len(info) != 1 || info[0].Domain != a2a.ErrorDomain || info[0].Type != a2a.ErrorInfoType {
		t.Fatalf("error data is not one enclave ErrorInfo: %s", ae.Data)
	}
}

func admit(t *testing.T, tbl *Table, req a2aagent.AdmitRequest) a2aagent.Admission {
	t.Helper()
	adm, err := tbl.Admit(context.Background(), req)
	if err != nil {
		t.Fatalf("Admit: %s", describe(err))
	}
	return adm
}

func req(p a2aagent.PrincipalRef, msgID, content string) a2aagent.AdmitRequest {
	return a2aagent.AdmitRequest{Principal: p, Epoch: "e1", MessageID: msgID, ContentHash: "sha256:" + content}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test path.
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

func TestAdmitPersistsBeforeReturning(t *testing.T) {
	dir := t.TempDir()
	c := newClock()
	tbl := openTable(t, testOptions(dir, c))
	adm := admit(t, tbl, req(orch, "m1", "aa"))
	if adm.Existing || adm.QueuePosition != 0 || adm.Task.State != a2a.TaskStateSubmitted || adm.Task.Seq != 1 {
		t.Fatalf("unexpected admission %+v", adm)
	}
	if len(adm.Task.TaskID) != 32 || len(adm.Task.ContextID) != 32 {
		t.Fatalf("IDs must carry 128 random bits: %q %q", adm.Task.TaskID, adm.Task.ContextID)
	}

	var task a2aagent.TaskRecord
	readJSON(t, filepath.Join(dir, a2aagent.TasksDirName, adm.Task.TaskID+".json"), &task)
	if task.SchemaVersion != "1" || task.Worker != "worker-1" || task.State != a2a.TaskStateSubmitted || task.UpdatedAt != "2026-11-05T12:00:00Z" {
		t.Fatalf("task file: %+v", task)
	}
	var dd a2aagent.DedupeFileData
	readJSON(t, filepath.Join(dir, a2aagent.DedupeFile), &dd)
	if len(dd.Records) != 1 || dd.Records[0].TaskID != task.TaskID || dd.Records[0].ContentHash != "sha256:aa" {
		t.Fatalf("dedupe file: %+v", dd)
	}
	var cf a2aagent.ContextsFileData
	readJSON(t, filepath.Join(dir, a2aagent.ContextsFile), &cf)
	if len(cf.Contexts) != 1 || cf.Contexts[0].ContextID != task.ContextID || cf.Contexts[0].Principal != orch {
		t.Fatalf("contexts file: %+v", cf)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}

	second := admit(t, tbl, a2aagent.AdmitRequest{Principal: orch, MessageID: "m2", ContentHash: "sha256:bb", ContextID: task.ContextID})
	if second.QueuePosition != 1 || second.Task.ContextID != task.ContextID {
		t.Fatalf("second admission %+v", second)
	}
	reopened := openTable(t, testOptions(dir, c))
	if got := reopened.Tasks(); len(got) != 2 || got[0].TaskID != task.TaskID {
		t.Fatalf("reopen lost tasks: %+v", got)
	}
	third := admit(t, reopened, req(orch, "m3", "cc"))
	if third.Task.Seq != 3 {
		t.Fatalf("seq not resumed: %d", third.Task.Seq)
	}
}

func TestDedupe(t *testing.T) {
	c := newClock()
	tbl := openTable(t, testOptions(t.TempDir(), c))
	orig := admit(t, tbl, req(orch, "m1", "aa"))

	retry := req(orch, "m1", "aa")
	retry.Retry = true
	got := admit(t, tbl, retry)
	if !got.Existing || got.Task.TaskID != orig.Task.TaskID {
		t.Fatalf("flagged retry must return the original: %+v", got)
	}

	_, err := tbl.Admit(context.Background(), req(orch, "m1", "zz"))
	wantCode(t, err, a2a.CodeMessageIDConflict)
	retry.ContentHash = "sha256:zz"
	_, err = tbl.Admit(context.Background(), retry)
	wantCode(t, err, a2a.CodeMessageIDConflict)
	_, err = tbl.Admit(context.Background(), req(orch, "m1", "aa"))
	wantCode(t, err, a2a.CodeMessageIDConflict)

	unknown := req(orch, "never-seen", "aa")
	unknown.Retry = true
	_, err = tbl.Admit(context.Background(), unknown)
	wantCode(t, err, a2a.CodeRetryAmbiguous)

	foreign := req(orchNew, "m1", "aa")
	foreign.Retry = true
	_, err = tbl.Admit(context.Background(), foreign)
	wantCode(t, err, a2a.CodeRetryAmbiguous)

	// The dedupe key is per principal: another principal may use the same message ID.
	admit(t, tbl, req(host, "m1", "aa"))

	// Retry is answered from the persisted record after a restart.
	reopened := openTable(t, testOptions(tbl.opts.Dir, c))
	retry.ContentHash = "sha256:aa"
	if got := admit(t, reopened, retry); !got.Existing || got.Task.TaskID != orig.Task.TaskID {
		t.Fatalf("retry after reopen: %+v", got)
	}
}

func TestEvictedOriginalMakesRetryAmbiguous(t *testing.T) {
	c := newClock()
	opts := testOptions(t.TempDir(), c)
	opts.RecordMaxAge = time.Hour
	tbl := openTable(t, opts)
	adm := admit(t, tbl, req(orch, "m1", "aa"))
	rec := adm.Task
	rec.State = a2a.TaskStateCompleted
	rec.EndedAt = formatTime(c.Now())
	if err := tbl.UpdateTask(rec); err != nil {
		t.Fatal(describe(err))
	}
	c.Advance(2 * time.Hour)
	if err := tbl.Expire(c.Now()); err != nil {
		t.Fatal(describe(err))
	}
	retry := req(orch, "m1", "aa")
	retry.Retry = true
	_, err := tbl.Admit(context.Background(), retry)
	wantCode(t, err, a2a.CodeRetryAmbiguous)
	if _, err := os.Stat(filepath.Join(opts.Dir, a2aagent.TasksDirName, rec.TaskID+".json")); !os.IsNotExist(err) {
		t.Fatalf("evicted task file still present: %v", err)
	}
}

func TestBindings(t *testing.T) {
	c := newClock()
	tbl := openTable(t, testOptions(t.TempDir(), c))
	adm := admit(t, tbl, req(orch, "m1", "aa"))

	if _, err := tbl.Task(orch, adm.Task.TaskID); err != nil {
		t.Fatal(describe(err))
	}
	_, err := tbl.Task(orchNew, adm.Task.TaskID)
	wantCode(t, err, a2a.CodeTaskNotFound)
	_, err = tbl.Task(host, adm.Task.TaskID)
	wantCode(t, err, a2a.CodeTaskNotFound)
	_, err = tbl.Task(orch, "missing")
	wantCode(t, err, a2a.CodeTaskNotFound)

	// A replacement orchestrator cannot resume the old generation's context.
	other := req(orchNew, "m2", "bb")
	other.ContextID = adm.Task.ContextID
	_, err = tbl.Admit(context.Background(), other)
	wantCode(t, err, a2a.CodeContextUnknown)
	_, err = tbl.Context(orchNew, adm.Task.ContextID)
	wantCode(t, err, a2a.CodeContextUnknown)
	unknown := req(orch, "m3", "cc")
	unknown.ContextID = "no-such-context"
	_, err = tbl.Admit(context.Background(), unknown)
	wantCode(t, err, a2a.CodeContextUnknown)

	if err := tbl.BindHarnessSession(adm.Task.ContextID, "sess-1", []string{"projects/x/sess-1.jsonl"}); err != nil {
		t.Fatal(describe(err))
	}
	if err := tbl.BindHarnessSession(adm.Task.ContextID, "sess-1", []string{"projects/x/sess-1.jsonl"}); err != nil {
		t.Fatal(describe(err))
	}
	cr, err := tbl.Context(orch, adm.Task.ContextID)
	if err != nil || cr.HarnessSessionID != "sess-1" || len(cr.Transcripts) != 1 {
		t.Fatalf("context %+v %s", cr, describe(err))
	}

	rec := adm.Task
	rec.Principal = host
	if err := tbl.UpdateTask(rec); err == nil {
		t.Fatal("UpdateTask must refuse to rebind a task")
	}
}

func TestRestrictions(t *testing.T) {
	c := newClock()
	dir := t.TempDir()
	tbl := openTable(t, testOptions(dir, c))
	queued := admit(t, tbl, req(orch, "m1", "aa"))
	hostTask := admit(t, tbl, req(host, "h1", "aa"))

	if err := tbl.StopAdmission(orch, StopSuspected); err != nil {
		t.Fatal(err)
	}
	_, err := tbl.Admit(context.Background(), req(orch, "m2", "bb"))
	wantCode(t, err, a2a.CodeAdmissionStopped)
	next, _ := tbl.Next()
	if next == nil || next.TaskID != hostTask.Task.TaskID {
		t.Fatalf("Next must skip a stopped principal: %+v", next)
	}
	if err := tbl.StopAdmission(orch, StopAuthoritative); err != nil {
		t.Fatal(err)
	}
	if err := tbl.StopAdmission(orch, StopSuspected); err != nil {
		t.Fatal(err)
	}
	if err := tbl.StopAdmission(orch, "bogus"); err == nil {
		t.Fatal("invalid reason accepted")
	}

	if err := tbl.SetQuarantine(&a2aagent.Quarantine{TaskID: "t1", PGID: 42, Reason: "termination-unconfirmed"}); err != nil {
		t.Fatal(err)
	}
	_ = tbl.SetQuarantine(&a2aagent.Quarantine{TaskID: "t2", Reason: "second"})
	_, err = tbl.Admit(context.Background(), req(host, "h2", "bb"))
	wantCode(t, err, a2a.CodeQuarantined)
	if next, _ := tbl.Next(); next != nil {
		t.Fatalf("Next must return nothing while quarantined: %+v", next)
	}

	// Restrictions survive a restart and never expire.
	c.Advance(30 * 24 * time.Hour)
	reopened := openTable(t, testOptions(dir, c))
	if err := reopened.Expire(c.Now()); err != nil {
		t.Fatal(describe(err))
	}
	r, _ := reopened.Restrictions()
	if len(r.AdmissionStops) != 1 || r.AdmissionStops[0].Reason != StopAuthoritative || r.AdmissionStops[0].Since != "2026-11-05T12:00:00Z" {
		t.Fatalf("admission stops: %+v", r.AdmissionStops)
	}
	if r.Quarantine == nil || r.Quarantine.TaskID != "t1" || r.Quarantine.PGID != 42 || r.Quarantine.Since == "" {
		t.Fatalf("quarantine: %+v", r.Quarantine)
	}

	if err := reopened.SetQuarantine(nil); err != nil {
		t.Fatal(err)
	}
	if err := reopened.ReleaseAdmission(orch); err != nil {
		t.Fatal(err)
	}
	if next, _ := reopened.Next(); next == nil || next.TaskID != queued.Task.TaskID {
		t.Fatalf("Next after release: %+v", next)
	}
	var rf a2aagent.RestrictionsFileData
	readJSON(t, filepath.Join(dir, a2aagent.RestrictionsFile), &rf)
	if rf.Quarantine != nil || len(rf.AdmissionStops) != 0 {
		t.Fatalf("restrictions file after release: %+v", rf)
	}
}

func TestHeartbeat(t *testing.T) {
	c := newClock()
	dir := t.TempDir()
	tbl := openTable(t, testOptions(dir, c))
	if err := tbl.Heartbeat(c.Now().Add(90 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var rf a2aagent.RestrictionsFileData
	readJSON(t, filepath.Join(dir, a2aagent.RestrictionsFile), &rf)
	if rf.UpdatedAt != "2026-11-05T12:01:30Z" || rf.AdmissionStops == nil {
		t.Fatalf("heartbeat: %+v", rf)
	}
}

func TestIdleTTLNeverExpiresActiveContexts(t *testing.T) {
	c := newClock()
	dir := t.TempDir()
	store := t.TempDir()
	opts := testOptions(dir, c)
	opts.ContextTTL = time.Hour
	opts.TranscriptRoot = store
	tbl := openTable(t, opts)

	queued := admit(t, tbl, req(orch, "m1", "aa"))
	running := admit(t, tbl, req(orch, "m2", "bb"))
	idle := admit(t, tbl, req(orch, "m3", "cc"))
	for _, rec := range []*a2aagent.TaskRecord{running.Task, idle.Task} {
		rec.State = a2a.TaskStateWorking
		if rec == idle.Task {
			rec.State = a2a.TaskStateCompleted
		}
		if err := tbl.UpdateTask(rec); err != nil {
			t.Fatal(describe(err))
		}
	}
	transcript := filepath.Join("projects", "p", "s.jsonl")
	if err := os.MkdirAll(filepath.Join(store, "projects", "p"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, transcript), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tbl.BindHarnessSession(idle.Task.ContextID, "s", []string{transcript}); err != nil {
		t.Fatal(err)
	}

	c.Advance(59 * time.Minute)
	if err := tbl.Expire(c.Now()); err != nil {
		t.Fatal(describe(err))
	}
	if _, err := tbl.Context(orch, idle.Task.ContextID); err != nil {
		t.Fatalf("expired before the TTL: %s", describe(err))
	}

	c.Advance(48 * time.Hour)
	if err := tbl.Expire(c.Now()); err != nil {
		t.Fatal(describe(err))
	}
	for _, id := range []string{queued.Task.ContextID, running.Task.ContextID} {
		if _, err := tbl.Context(orch, id); err != nil {
			t.Fatalf("active context %s expired: %s", id, describe(err))
		}
	}
	_, err := tbl.Context(orch, idle.Task.ContextID)
	wantCode(t, err, a2a.CodeContextExpired)
	_, err = tbl.Context(host, idle.Task.ContextID)
	wantCode(t, err, a2a.CodeContextUnknown)
	resume := req(orch, "m4", "dd")
	resume.ContextID = idle.Task.ContextID
	_, err = tbl.Admit(context.Background(), resume)
	wantCode(t, err, a2a.CodeContextExpired)
	resume.Principal = host
	_, err = tbl.Admit(context.Background(), resume)
	wantCode(t, err, a2a.CodeContextUnknown)
	if _, err := os.Stat(filepath.Join(store, transcript)); !os.IsNotExist(err) {
		t.Fatalf("transcript not deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store, "projects", "p")); err != nil {
		t.Fatalf("expiry deleted more than the transcript: %v", err)
	}

	// Enqueue refreshes activity, so a resumed context restarts its TTL.
	follow := req(orch, "m5", "ee")
	follow.ContextID = queued.Task.ContextID
	admit(t, tbl, follow)
	cr, _ := tbl.Context(orch, queued.Task.ContextID)
	if cr.LastActivityAt != formatTime(c.Now()) {
		t.Fatalf("lastActivityAt not updated at enqueue: %s", cr.LastActivityAt)
	}
}

func TestTranscriptPathsOutsideStoreAreRefused(t *testing.T) {
	c := newClock()
	opts := testOptions(t.TempDir(), c)
	opts.ContextTTL = time.Minute
	outside := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	opts.TranscriptRoot = t.TempDir()
	tbl := openTable(t, opts)
	adm := admit(t, tbl, req(orch, "m1", "aa"))
	rec := adm.Task
	rec.State = a2a.TaskStateCompleted
	_ = tbl.UpdateTask(rec)
	_ = tbl.BindHarnessSession(rec.ContextID, "s", []string{"../" + filepath.Base(filepath.Dir(outside)) + "/keep", outside})
	c.Advance(time.Hour)
	if err := tbl.Expire(c.Now()); err == nil {
		t.Fatal("non-local transcript paths must be reported")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("file outside the store was deleted: %v", err)
	}
}

func TestRecordBounds(t *testing.T) {
	c := newClock()
	opts := testOptions(t.TempDir(), c)
	opts.RecordLimit = 3
	opts.QueueDepth = 100
	opts.ContextTTL = time.Minute
	tbl := openTable(t, opts)
	var ids []string
	for i := range 3 {
		adm := admit(t, tbl, req(orch, fmt.Sprintf("m%d", i), "aa"))
		rec := adm.Task
		rec.State = a2a.TaskStateFailed
		rec.EndedAt = formatTime(c.Now())
		_ = tbl.UpdateTask(rec)
		ids = append(ids, rec.TaskID)
		c.Advance(time.Second)
	}
	// At the limit, admission evicts the oldest terminal record.
	admit(t, tbl, req(orch, "m3", "aa"))
	if _, err := tbl.Task(orch, ids[0]); err == nil {
		t.Fatal("oldest terminal task not evicted")
	}
	if _, err := tbl.Task(orch, ids[1]); err != nil {
		t.Fatalf("newer task evicted: %s", describe(err))
	}
	// Active records are never evicted, so a table full of them refuses admission.
	admit(t, tbl, req(orch, "m4", "aa"))
	admit(t, tbl, req(orch, "m5", "aa"))
	_, err := tbl.Admit(context.Background(), req(orch, "m6", "aa"))
	wantCode(t, err, a2a.CodeQueueFull)

	c.Advance(time.Hour)
	if err := tbl.Expire(c.Now()); err != nil {
		t.Fatal(describe(err))
	}
	if n := len(tbl.Tasks()); n != 3 {
		t.Fatalf("active tasks evicted: %d left", n)
	}
	if n := len(tbl.dedupe.Records); n != 3 {
		t.Fatalf("dedupe records: want 3 (active only), got %d", n)
	}
	if n := len(tbl.contexts.Tombstones); n != 3 {
		t.Fatalf("tombstones: want the limit 3, got %d", n)
	}
	c.Advance(8 * 24 * time.Hour)
	_ = tbl.Expire(c.Now())
	if n := len(tbl.contexts.Tombstones); n != 0 {
		t.Fatalf("tombstones past the age bound: %d", n)
	}
}

func TestQueueDepth(t *testing.T) {
	c := newClock()
	opts := testOptions(t.TempDir(), c)
	opts.QueueDepth = 2
	tbl := openTable(t, opts)
	first := admit(t, tbl, req(orch, "m1", "aa"))
	first.Task.State = a2a.TaskStateWorking
	_ = tbl.UpdateTask(first.Task)
	if got := admit(t, tbl, req(orch, "m2", "aa")); got.QueuePosition != 1 {
		t.Fatalf("position behind a running task: %d", got.QueuePosition)
	}
	if got := admit(t, tbl, req(orch, "m3", "aa")); got.QueuePosition != 2 {
		t.Fatalf("position: %d", got.QueuePosition)
	}
	_, err := tbl.Admit(context.Background(), req(orch, "m4", "aa"))
	wantCode(t, err, a2a.CodeQueueFull)
}

func TestOpenRepairsInterruptedAdmission(t *testing.T) {
	c := newClock()
	dir := t.TempDir()
	tbl := openTable(t, testOptions(dir, c))
	adm := admit(t, tbl, req(orch, "m1", "aa"))
	// Simulate a crash after the task file and before the dedupe and context writes.
	empty := a2aagent.DedupeFileData{TableHeader: tbl.header(c.Now()), Records: []a2aagent.DedupeRecord{}}
	if err := tbl.writeJSON(a2aagent.DedupeFile, &empty); err != nil {
		t.Fatal(err)
	}
	noCtx := a2aagent.ContextsFileData{TableHeader: tbl.header(c.Now())}
	if err := tbl.writeJSON(a2aagent.ContextsFile, &noCtx); err != nil {
		t.Fatal(err)
	}
	reopened := openTable(t, testOptions(dir, c))
	retry := req(orch, "m1", "aa")
	retry.Retry = true
	if got := admit(t, reopened, retry); !got.Existing || got.Task.TaskID != adm.Task.TaskID {
		t.Fatalf("dedupe record not rebuilt: %+v", got)
	}
	if _, err := reopened.Context(orch, adm.Task.ContextID); err != nil {
		t.Fatalf("context not rebuilt: %s", describe(err))
	}
}

func TestOpenRejectsForeignOrUnknownFiles(t *testing.T) {
	c := newClock()
	dir := t.TempDir()
	openTable(t, testOptions(dir, c))
	other := testOptions(dir, c)
	other.Header.WorkerGeneration = "0000000000000def"
	if _, err := Open(other); err == nil {
		t.Fatal("table of another worker generation accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, a2aagent.DedupeFile), []byte(`{"schemaVersion":"2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(testOptions(dir, c)); err == nil || !strings.Contains(err.Error(), "schemaVersion") {
		t.Fatalf("unknown schemaVersion accepted: %v", err)
	}
	if _, err := Open(Options{Dir: dir}); err == nil {
		t.Fatal("missing header accepted")
	}
}

func TestAdmitRejectsIncompleteRequests(t *testing.T) {
	tbl := openTable(t, testOptions(t.TempDir(), newClock()))
	for _, r := range []a2aagent.AdmitRequest{
		{Principal: orch, MessageID: "m", ContentHash: "md5:x"},
		{Principal: orch, ContentHash: "sha256:x"},
		{Principal: a2aagent.PrincipalRef{Member: "x"}, MessageID: "m", ContentHash: "sha256:x"},
	} {
		_, err := tbl.Admit(context.Background(), r)
		wantCode(t, err, a2a.CodeInvalidParams)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tbl.Admit(ctx, req(orch, "m", "x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context: %s", describe(err))
	}
}
