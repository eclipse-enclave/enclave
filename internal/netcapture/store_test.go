// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netcapture

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTestStore(t *testing.T, opts Options) *Store {
	t.Helper()
	if opts.Dir == "" {
		opts.Dir = t.TempDir()
	}
	store, err := Open(opts)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return store
}

// captureExchange runs one exchange through a capture the way the proxy does:
// the upstream reads the request body, then the response body is relayed.
func captureExchange(t *testing.T, store *Store, requestBody string, responseBody string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://api.example.com/v1/items?filter=all", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	capture := store.Begin(req, "https")
	if capture == nil {
		t.Fatal("Begin() = nil")
	}
	if _, err := io.Copy(io.Discard, capture.RequestBody(req.Body)); err != nil {
		t.Fatalf("read request body: %v", err)
	}
	capture.Response(&http.Response{StatusCode: http.StatusOK, Proto: "HTTP/1.1", Header: http.Header{"Content-Type": {"text/plain"}}})
	if _, err := io.WriteString(capture.ResponseBody(), responseBody); err != nil {
		t.Fatalf("write response body: %v", err)
	}
	id := capture.Finish(nil)
	if id == "" {
		t.Fatal("Finish() returned no ID")
	}
	return id
}

func readBody(t *testing.T, recordPath string, body *Body) string {
	t.Helper()
	path, ok := BodyPath(recordPath, body)
	if !ok {
		t.Fatalf("body %+v has no file", body)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- test-owned temp path.
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw)
}

func TestStoreWritesRecordAndBodies(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, Options{Dir: dir, Session: "enclave-demo-1"})

	id := captureExchange(t, store, `{"name":"item"}`, "created")

	recordPath := RecordPath(dir, id)
	record, err := Load(recordPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if record.SchemaVersion != SchemaVersion || record.Kind != Kind || record.ID != id || record.Session != "enclave-demo-1" {
		t.Fatalf("record identity = %+v", record)
	}
	if record.Request.Method != http.MethodPost || record.Request.URL != "https://api.example.com/v1/items?filter=all" {
		t.Fatalf("request line = %s %s", record.Request.Method, record.Request.URL)
	}
	if record.Request.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("request headers = %v", record.Request.Headers)
	}
	if got := readBody(t, recordPath, record.Request.Body); got != `{"name":"item"}` {
		t.Fatalf("request body = %q", got)
	}
	if record.Response == nil || record.Response.Status != http.StatusOK || record.Response.Headers.Get("Content-Type") != "text/plain" {
		t.Fatalf("response = %+v", record.Response)
	}
	if got := readBody(t, recordPath, record.Response.Body); got != "created" {
		t.Fatalf("response body = %q", got)
	}
	if record.Request.Body.Truncated || record.Response.Body.Truncated {
		t.Fatal("bodies below the cap were marked truncated")
	}
}

func TestStoreTruncatesBodiesAtTheCap(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, Options{Dir: dir, MaxBodyBytes: 4})

	id := captureExchange(t, store, "0123456789", "abcdefgh")

	recordPath := RecordPath(dir, id)
	record, err := Load(recordPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for name, tc := range map[string]struct {
		body *Body
		full int64
		want string
	}{
		"request":  {record.Request.Body, 10, "0123"},
		"response": {record.Response.Body, 8, "abcd"},
	} {
		if !tc.body.Truncated || tc.body.Bytes != tc.full || tc.body.StoredBytes != 4 {
			t.Fatalf("%s body = %+v, want truncated at 4 of %d bytes", name, tc.body, tc.full)
		}
		if got := readBody(t, recordPath, tc.body); got != tc.want {
			t.Fatalf("%s body = %q, want %q", name, got, tc.want)
		}
	}
}

func TestStoreRedactsReleasedSecrets(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, Options{Dir: dir, Secrets: []Secret{{Value: "sk-live-value", Placeholder: "ENCLAVE_PH"}}})

	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/echo?key=sk-live-value", nil)
	req.Header.Set("Authorization", "Bearer ENCLAVE_PH")
	capture := store.Begin(req, "https")
	capture.Response(&http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Echo": {"Bearer sk-live-value"}}})
	sink := capture.ResponseBody()
	_, _ = io.WriteString(sink, `{"authorization":"Bearer sk-li`)
	_, _ = io.WriteString(sink, `ve-value"}`)
	id := capture.Finish(nil)

	raw, err := os.ReadFile(RecordPath(dir, id)) // #nosec G304 -- test-owned temp path.
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	if strings.Contains(string(raw), "sk-live-value") {
		t.Fatalf("record contains the secret value:\n%s", raw)
	}
	record, _ := Load(RecordPath(dir, id))
	if record.Request.URL != "https://api.example.com/echo?key=ENCLAVE_PH" || record.Response.Headers.Get("X-Echo") != "Bearer ENCLAVE_PH" {
		t.Fatalf("record = %+v", record)
	}
	if got := readBody(t, RecordPath(dir, id), record.Response.Body); got != `{"authorization":"Bearer ENCLAVE_PH"}` {
		t.Fatalf("response body = %q", got)
	}
}

func TestStoreEvictsOldestCapturesOverTheBudget(t *testing.T) {
	dir := t.TempDir()
	// Each capture stores 2 KiB of bodies plus a record of well under 1 KiB, so
	// the budget holds two of them but not three.
	store := openTestStore(t, Options{Dir: dir, MaxBytes: 6 * 1024})
	body := strings.Repeat("x", 1024)

	ids := []string{
		captureExchange(t, store, body, body),
		captureExchange(t, store, body, body),
		captureExchange(t, store, body, body),
	}

	if _, err := os.Stat(RecordPath(dir, ids[0])); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oldest capture survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ids[0]+requestBodySuffix)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oldest capture's body survived: %v", err)
	}
	for _, id := range ids[1:] {
		if _, err := os.Stat(RecordPath(dir, id)); err != nil {
			t.Fatalf("recent capture %s was evicted: %v", id, err)
		}
	}
}

func TestOpenCountsExistingCapturesAndRemovesOrphans(t *testing.T) {
	dir := t.TempDir()
	first := openTestStore(t, Options{Dir: dir})
	kept := captureExchange(t, first, "request", "response")

	orphan := filepath.Join(dir, "0123456789abcdef"+responseBodySuffix)
	staged := filepath.Join(dir, ".0123456789abcdef.json.123")
	unrelated := filepath.Join(dir, "notes.txt")
	for _, path := range []string{orphan, staged, unrelated} {
		if err := os.WriteFile(path, []byte("left behind"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	second := openTestStore(t, Options{Dir: dir})
	if second.used == 0 || len(second.finished) != 1 || second.finished[0].id != kept {
		t.Fatalf("reopened store = used %d, finished %+v", second.used, second.finished)
	}
	for _, path := range []string{orphan, staged} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived reopening: %v", filepath.Base(path), err)
		}
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated file was removed: %v", err)
	}
}

func TestFinishWithoutWritableDirectoryReportsNoCapture(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, Options{Dir: dir})
	req := httptest.NewRequest(http.MethodGet, "http://api.example.com/", nil)
	capture := store.Begin(req, "http")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
	if id := capture.Finish(nil); id != "" {
		t.Fatalf("Finish() = %q, want no ID when the record cannot be written", id)
	}
}

func TestFinishRecordsRelayError(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, Options{Dir: dir})
	capture := store.Begin(httptest.NewRequest(http.MethodGet, "http://api.example.com/", nil), "http")
	id := capture.Finish(errors.New("dial tcp: connection refused"))
	record, err := Load(RecordPath(dir, id))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if record.Error != "dial tcp: connection refused" || record.Response != nil || record.Request.Body != nil {
		t.Fatalf("record = %+v", record)
	}
}

func TestNilStoreIsANoOp(t *testing.T) {
	store, err := Open(Options{})
	if err != nil || store != nil {
		t.Fatalf("Open(empty) = %v, %v; want a nil store", store, err)
	}
	capture := store.Begin(httptest.NewRequest(http.MethodGet, "http://example.com/", nil), "http")
	if capture != nil {
		t.Fatal("nil store handed out a capture")
	}
	body := io.NopCloser(strings.NewReader("x"))
	if capture.RequestBody(body) != body || capture.ResponseBody() != nil {
		t.Fatal("nil capture wrapped a body")
	}
	capture.Response(&http.Response{})
	if capture.Finish(nil) != "" {
		t.Fatal("nil capture reported an ID")
	}
}

// Bodies still in flight draw from the session budget as they are written, so
// many open streams cannot store more than it allows.
func TestStoreBudgetBoundsBodiesInFlight(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, Options{Dir: dir, MaxBytes: 2048})
	finished := captureExchange(t, store, "", strings.Repeat("f", 512))

	var open []*Capture
	for i := 0; i < 10; i++ {
		capture := store.Begin(httptest.NewRequest(http.MethodGet, "http://api.example.com/stream", nil), "http")
		capture.Response(&http.Response{StatusCode: http.StatusOK, Header: http.Header{}})
		_, _ = io.WriteString(capture.ResponseBody(), strings.Repeat("s", 1024))
		open = append(open, capture)
	}

	var stored int64
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", entry.Name(), err)
		}
		stored += info.Size()
	}
	if stored > 2048 {
		t.Fatalf("store holds %d bytes with captures in flight, over the 2048-byte budget", stored)
	}
	if _, err := os.Stat(RecordPath(dir, finished)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the finished capture was not evicted to make room: %v", err)
	}

	truncated := 0
	for _, capture := range open {
		// Finishing books the record and may evict earlier captures, so each one
		// is read straight away.
		id := capture.Finish(nil)
		record, err := Load(RecordPath(dir, id))
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if body := record.Response.Body; body.Truncated {
			truncated++
			if body.Bytes != 1024 {
				t.Fatalf("truncated body = %+v, want all 1024 bytes counted", body)
			}
		}
	}
	if truncated == 0 {
		t.Fatal("no body was marked truncated although the budget ran out")
	}
}
