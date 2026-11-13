// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"enclave/internal/filter"
	"enclave/internal/filter/spec"
)

func TestLedgerKeyed(t *testing.T) {
	l := NewLedger(map[string]int64{"read": 2}, map[string]bool{"read": true})
	a, exceeded := l.Reserve([]Need{{Counter: "read", Keys: []string{"a"}}})
	if exceeded != nil {
		t.Fatal(exceeded)
	}
	// The same key in flight takes no second slot.
	a2, exceeded := l.Reserve([]Need{{Counter: "read", Keys: []string{"a", "a"}}})
	if exceeded != nil {
		t.Fatal(exceeded)
	}
	b, exceeded := l.Reserve([]Need{{Counter: "read", Keys: []string{"b"}}})
	if exceeded != nil {
		t.Fatal(exceeded)
	}
	// Pending keys count against the quota.
	if _, exceeded := l.Reserve([]Need{{Counter: "read", Keys: []string{"c"}}}); exceeded == nil || exceeded.Counter != "read" || exceeded.Limit != 2 {
		t.Fatalf("Reserve(c) = %v, want exceeded", exceeded)
	}
	a.Release()
	a.Release()
	// a is still held by a2.
	if _, exceeded := l.Reserve([]Need{{Counter: "read", Keys: []string{"c"}}}); exceeded == nil {
		t.Fatal("released key freed a slot another request still holds")
	}
	a2.Commit()
	b.Release()
	b.Commit()
	if got := l.Usage()["read"]; got != 1 {
		t.Fatalf("usage = %d, want 1", got)
	}
	// Committed keys never count twice, even at the quota.
	c, exceeded := l.Reserve([]Need{{Counter: "read", Keys: []string{"c"}}})
	if exceeded != nil {
		t.Fatal(exceeded)
	}
	c.Commit()
	if _, exceeded := l.Reserve([]Need{{Counter: "read", Keys: []string{"a", "c"}}}); exceeded != nil {
		t.Fatalf("re-reading committed keys = %v", exceeded)
	}
	if _, exceeded := l.Reserve([]Need{{Counter: "read", Keys: []string{"a", "d"}}}); exceeded == nil {
		t.Fatal("new key beyond the quota accepted")
	}
	if _, exceeded := l.Reserve([]Need{{Counter: "read"}}); exceeded != nil {
		t.Fatalf("empty key list = %v", exceeded)
	}
}

func TestLedgerUnkeyedAndAtomic(t *testing.T) {
	l := NewLedger(map[string]int64{"sent": 1, "written": 5}, nil)
	r1, exceeded := l.Reserve([]Need{{Counter: "sent"}, {Counter: "written"}})
	if exceeded != nil {
		t.Fatal(exceeded)
	}
	// All or nothing: sent is full, so written must not be reserved either.
	if _, exceeded := l.Reserve([]Need{{Counter: "written"}, {Counter: "sent"}}); exceeded == nil || exceeded.Counter != "sent" {
		t.Fatalf("Reserve = %v", exceeded)
	}
	r1.Commit()
	if u := l.Usage(); u["sent"] != 1 || u["written"] != 1 {
		t.Fatalf("usage = %v", u)
	}
	// Two needs of one unkeyed counter in one request take two slots.
	if _, exceeded := NewLedger(map[string]int64{"w": 1}, nil).Reserve([]Need{{Counter: "w"}, {Counter: "w"}}); exceeded == nil {
		t.Fatal("double need fit a quota of one")
	}
	// Counters without a quota are not tracked.
	if res, exceeded := l.Reserve([]Need{{Counter: "unlimited"}}); exceeded != nil || len(res.items) != 0 {
		t.Fatalf("untracked counter = %v, %v", res, exceeded)
	}
	var nilRes *Reservation
	nilRes.Commit()
}

func TestLedgerZeroQuotaBlocksEverything(t *testing.T) {
	l := NewLedger(map[string]int64{"read": 0, "sent": 0}, map[string]bool{"read": true})
	if _, exceeded := l.Reserve([]Need{{Counter: "read"}}); exceeded == nil || exceeded.Limit != 0 {
		t.Fatal("keyed counter with quota 0 accepted an empty key list")
	}
	if _, exceeded := l.Reserve([]Need{{Counter: "sent"}}); exceeded == nil {
		t.Fatal("unkeyed counter with quota 0 accepted")
	}
}

// I7: concurrent requests never exceed a quota.
func TestLedgerConcurrent(t *testing.T) {
	l := NewLedger(map[string]int64{"read": 25, "sent": 10}, map[string]bool{"read": true})
	var granted, sent atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, exceeded := l.Reserve([]Need{{Counter: "read", Keys: []string{fmt.Sprint(i % 50)}}, {Counter: "sent"}})
			if exceeded != nil {
				return
			}
			if i%3 == 0 {
				res.Release()
				return
			}
			granted.Add(1)
			sent.Add(1)
			res.Commit()
		}(i)
	}
	wg.Wait()
	u := l.Usage()
	if u["read"] > 25 || u["sent"] > 10 || u["sent"] != sent.Load() {
		t.Fatalf("usage = %v, sent %d", u, sent.Load())
	}
}

func quotaPreset(quota int) map[string]string {
	return map[string]string{"tight": fmt.Sprintf("extends: send-eclipsesource\nquotas:\n  mail.read: %d\n  mail.sent: 1\n", quota)}
}

func readMessage(t *testing.T, h *harness, id string, status int) *Decision {
	t.Helper()
	d := h.eval(t, newRequest("GET", gmail+"/gmail/v1/users/me/messages/"+id, ""))
	if d.Denial == nil {
		d.Finish(status)
	}
	return d
}

func TestEngineQuotaOnRequestKeys(t *testing.T) {
	h := newHarness(t, "tight", quotaPreset(2))
	mustAllow(t, readMessage(t, h, "m1", 200))
	mustAllow(t, readMessage(t, h, "m2", 500))
	mustAllow(t, readMessage(t, h, "m2", 200))
	d := mustDeny(t, readMessage(t, h, "m3", 200), ReasonQuota, "quota mail.read exhausted (2)")
	if d.Code != http.StatusForbidden || d.Status != filter.StatusResourceExhausted {
		t.Fatalf("quota denial = %d %s", d.Code, d.Status)
	}
	if !strings.Contains(string(d.Body), `"status":"RESOURCE_EXHAUSTED"`) {
		t.Fatalf("deny body = %s", d.Body)
	}
	mustAllow(t, readMessage(t, h, "m1", 200))

	// Unkeyed: one send per session.
	send := func() *Decision {
		return h.eval(t, newRequest("POST", sendURL, sendBody("To: a@eclipsesource.com"), withContentType("application/json")))
	}
	first := send()
	mustAllow(t, first)
	first.Finish(0)
	second := send()
	mustAllow(t, second)
	second.Finish(200)
	mustDeny(t, send(), ReasonQuota, "quota mail.sent exhausted (1)")
}

// A redirect may point at a host that serves the content without the filter,
// so it commits like a 2xx response.
func TestEngineQuotaCommitsOnRedirects(t *testing.T) {
	h := newHarness(t, "tight", quotaPreset(3))
	mustAllow(t, readMessage(t, h, "m1", http.StatusFound))
	mustAllow(t, readMessage(t, h, "m2", http.StatusNotModified))
	mustAllow(t, readMessage(t, h, "m3", http.StatusBadRequest))
	if got := h.engine.Ledger().Usage()["mail.read"]; got != 2 {
		t.Fatalf("usage = %d, want 2", got)
	}
}

func TestEngineQuotaZero(t *testing.T) {
	h := newHarness(t, "tight", quotaPreset(0))
	mustDeny(t, readMessage(t, h, "m1", 200), ReasonQuota, "exhausted (0)")
}

func threadResponse(status int, ids ...string) *http.Response {
	var parts []string
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf(`{"id":%q,"payload":{"body":"secret"}}`, id))
	}
	body := `{"id":"t1","messages":[` + strings.Join(parts, ",") + `]}`
	header := http.Header{}
	header.Set("Content-Type", "application/json; charset=UTF-8")
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), ContentLength: -1}
}

// I7: content counted from a response is withheld when it would exceed the
// quota.
func TestEngineQuotaOnResponse(t *testing.T) {
	h := newHarness(t, "tight", quotaPreset(3))
	mustAllow(t, readMessage(t, h, "a", 200))

	thread := func() *Decision {
		req := newRequest("GET", gmail+"/gmail/v1/users/me/threads/t1?format=full", "", withHeader("Accept-Encoding", "gzip"))
		d := h.eval(t, req)
		mustAllow(t, d)
		if req.Header.Get("Accept-Encoding") != "" || !d.InspectsResponse() {
			t.Fatal("response route did not drop Accept-Encoding")
		}
		return d
	}

	d := thread()
	resp := threadResponse(200, "a", "b", "c", "d")
	denial := d.InspectResponse(resp)
	if denial == nil || denial.Reason != ReasonQuota || !strings.Contains(denial.Message, "response withheld") {
		t.Fatalf("InspectResponse = %+v", denial)
	}
	d.Finish(200)
	if got := h.engine.Ledger().Usage()["mail.read"]; got != 1 {
		t.Fatalf("withheld response counted: usage %d", got)
	}

	d = thread()
	resp = threadResponse(200, "a", "b", "c")
	if denial := d.InspectResponse(resp); denial != nil {
		t.Fatalf("InspectResponse = %s", denial.Message)
	}
	body, _ := io.ReadAll(resp.Body)
	if int64(len(body)) != resp.ContentLength || resp.Header.Get("Content-Length") != strconv.Itoa(len(body)) || !strings.Contains(string(body), `"id":"c"`) {
		t.Fatalf("relayed body %q, length %d / %s", body, resp.ContentLength, resp.Header.Get("Content-Length"))
	}
	d.Finish(200)
	if got := h.engine.Ledger().Usage()["mail.read"]; got != 3 {
		t.Fatalf("usage = %d, want 3", got)
	}
	mustAllow(t, readMessage(t, h, "c", 200))
	mustDeny(t, readMessage(t, h, "e", 200), ReasonQuota, "exhausted")
}

func TestEngineResponseInspectionFailures(t *testing.T) {
	h := newHarness(t, "tight", quotaPreset(10))
	inspect := func(resp *http.Response) *Denial {
		d := h.eval(t, newRequest("GET", gmail+"/gmail/v1/users/me/threads/t1", ""))
		mustAllow(t, d)
		denial := d.InspectResponse(resp)
		d.Finish(resp.StatusCode)
		return denial
	}
	gz := threadResponse(200, "a")
	gz.Header.Set("Content-Encoding", "gzip")
	if denial := inspect(gz); denial == nil || denial.Reason != ReasonResponse {
		t.Fatalf("compressed response = %+v", denial)
	}
	bad := threadResponse(200)
	bad.Body = io.NopCloser(strings.NewReader(`{"messages":[{"id":{}}]}`))
	if denial := inspect(bad); denial == nil || denial.Reason != ReasonResponse {
		t.Fatalf("malformed response = %+v", denial)
	}
	// A response bind reads only JSON.
	text := threadResponse(200, "a")
	text.Header.Set("Content-Type", "text/plain")
	if denial := inspect(text); denial == nil || denial.Reason != ReasonResponse || !strings.Contains(denial.Message, "not declared as JSON") {
		t.Fatalf("text response = %+v", denial)
	}
	// A redirect cannot be counted, so it is withheld.
	redirect := threadResponse(http.StatusFound)
	redirect.Header.Set("Location", "https://download.example/t1")
	if denial := inspect(redirect); denial == nil || denial.Reason != ReasonResponse || !strings.Contains(denial.Message, "redirect") {
		t.Fatalf("redirect response = %+v", denial)
	}
	// Errors pass through uncounted.
	notFound := threadResponse(404)
	if denial := inspect(notFound); denial != nil {
		t.Fatalf("404 response = %+v", denial)
	}
	if got := h.engine.Ledger().Usage()["mail.read"]; got != 0 {
		t.Fatalf("usage = %d", got)
	}
}

const responseLimitSpec = `
schemaVersion: "1"
kind: filter
name: limits
hosts: [api.example.com]
credential:
  static: { env: [LIMITS_TOKEN] }
  inject: { header: authorization, format: "Bearer %s" }
  env: [LIMITS_TOKEN]
counters:
  items: {}
capabilities:
  read: {}
routes:
  - op: list
    capability: read
    method: GET
    host: api.example.com
    path: /items
    maxResponseBytes: 16
    bind:
      ids: [ {response: {}}, {json: "$.items[*]"} ]
    count:
      - { counter: items, key: ids }
`

func TestEngineResponseLimit(t *testing.T) {
	pol := parseSpecPolicy(t, responseLimitSpec, &spec.Preset{
		Capabilities: map[string]map[string]filter.ConstraintValue{"read": {}},
		Quotas:       map[string]int64{"items": 10},
	})
	h := newHarnessForPolicy(t, pol)
	d := h.eval(t, newRequest("GET", "https://api.example.com/items", ""))
	mustAllow(t, d)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"items":["a","b","c","d"]}`))}
	if denial := d.InspectResponse(resp); denial == nil || !strings.Contains(denial.Message, "inspection limit of 16 bytes") {
		t.Fatalf("oversized response = %+v", denial)
	}
}

// I7 through the engine: many concurrent reads of distinct messages never
// exceed the quota.
func TestEngineQuotaConcurrent(t *testing.T) {
	h := newHarness(t, "tight", quotaPreset(10))
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d := h.eval(t, newRequest("GET", gmail+"/gmail/v1/users/me/messages/m"+strconv.Itoa(i), ""))
			if d.Denial != nil {
				if d.Denial.Reason != ReasonQuota {
					t.Errorf("unexpected denial %s", d.Denial.Message)
				}
				return
			}
			allowed.Add(1)
			d.Finish(200)
		}(i)
	}
	wg.Wait()
	if allowed.Load() != 10 || h.engine.Ledger().Usage()["mail.read"] != 10 {
		t.Fatalf("allowed %d, usage %d, want 10", allowed.Load(), h.engine.Ledger().Usage()["mail.read"])
	}
}
