// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"enclave/internal/filter"
	"enclave/internal/filter/token"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testResolver(fetcher Fetcher, now func() time.Time) *resolver {
	extract, _ := filter.ParseJSONPath("$.parents[*]")
	return &resolver{
		name:    "driveParents",
		def:     filter.Resolver{URL: "https://www.googleapis.com/drive/v3/files/{id}?fields=parents", Extract: "$.parents[*]", Recursive: true, MaxDepth: 8},
		extract: extract,
		fetch:   fetcher,
		cache:   newParentCache(now),
	}
}

func TestResolverCacheTTL(t *testing.T) {
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	f := newFakeFetcher()
	r := testResolver(f, c.Now)
	targets := map[string]bool{"FOLDER1": true}
	for i := 0; i < 3; i++ {
		if ok, err := r.within(context.Background(), "FILE_DEEP", targets); !ok || err != nil {
			t.Fatalf("within = %v, %v", ok, err)
		}
	}
	if f.calls["FILE_DEEP"] != 1 || f.calls["SUB"] != 1 {
		t.Fatalf("calls = %v, want one lookup each", f.calls)
	}
	c.Advance(ResolverCacheTTL - time.Second)
	_, _ = r.within(context.Background(), "FILE_DEEP", targets)
	if f.calls["FILE_DEEP"] != 1 {
		t.Fatal("cache expired early")
	}
	c.Advance(time.Second)
	_, _ = r.within(context.Background(), "FILE_DEEP", targets)
	if f.calls["FILE_DEEP"] != 2 {
		t.Fatalf("cache did not expire after %s", ResolverCacheTTL)
	}
	// Failures are not cached.
	_, _ = r.within(context.Background(), "FILE_ERR", targets)
	_, _ = r.within(context.Background(), "FILE_ERR", targets)
	if f.calls["FILE_ERR"] != 2 {
		t.Fatalf("failed lookup cached: %d calls", f.calls["FILE_ERR"])
	}
}

func TestResolverCacheIsBounded(t *testing.T) {
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	cache := newParentCache(c.Now)
	cache.max = 16
	for i := 0; i < 100; i++ {
		cache.put("r", fmt.Sprint(i), []string{"p"})
		c.Advance(time.Millisecond)
	}
	if n := cache.len(); n > 16 {
		t.Fatalf("cache holds %d entries, max 16", n)
	}
	if _, ok := cache.get("r", "99"); !ok {
		t.Fatal("newest entry evicted")
	}
	if _, ok := cache.get("r", "0"); ok {
		t.Fatal("oldest entry kept")
	}
	if _, ok := cache.get("other", "99"); ok {
		t.Fatal("cache key ignores the resolver name")
	}
}

// I6: resolver errors never grant.
func TestResolverFailureSemantics(t *testing.T) {
	f := newFakeFetcher()
	r := testResolver(f, time.Now)
	targets := map[string]bool{"FOLDER1": true}
	tests := []struct {
		id      string
		ok      bool
		wantErr bool
	}{
		{"FILE_IN", true, false},
		{"FILE_ERR", false, true},
		{"FILE_UP", false, false},
		{"CYCLE_A", false, false},
		{"FILE_OUT", false, false},
		{"..", false, true},
		{"a/b", false, true},
		{"a?b", false, true},
		{"", false, true},
		{strings.Repeat("a", maxResolverIDLength+1), false, true},
	}
	for _, tt := range tests {
		ok, err := r.within(context.Background(), tt.id, targets)
		if ok != tt.ok || (err != nil) != tt.wantErr {
			t.Errorf("within(%q) = %v, %v, want %v, error %v", tt.id, ok, err, tt.ok, tt.wantErr)
		}
	}
	for _, u := range f.urls {
		if strings.Contains(u, "..") || strings.Count(u, "?") != 1 {
			t.Fatalf("unsafe lookup URL %q", u)
		}
	}
}

func TestResolverStopsAtLookupCap(t *testing.T) {
	f := newFakeFetcher()
	// Every node has many parents, none of them the target.
	for i := 0; i < 300; i++ {
		var parents []string
		for j := 0; j < 10; j++ {
			parents = append(parents, fmt.Sprintf("N%d", i*10+j+1))
		}
		f.parents[fmt.Sprintf("N%d", i)] = parents
	}
	r := testResolver(f, time.Now)
	ok, err := r.within(context.Background(), "N0", map[string]bool{"FOLDER1": true})
	total := 0
	for _, n := range f.calls {
		total += n
	}
	if ok || err != nil || total > maxLookupsPerValue {
		t.Fatalf("within = %v, %v after %d lookups", ok, err, total)
	}
}

func TestHTTPFetcher(t *testing.T) {
	var gotAuth, gotPath string
	status := http.StatusOK
	body := `{"parents":["P"]}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.EscapedPath()
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	defer srv.Close()
	tokens := &fakeTokens{value: realToken}
	f := &HTTPFetcher{Client: srv.Client(), Tokens: tokens, Inject: filter.Inject{Header: "authorization", Format: "Bearer %s"}}

	data, err := f.Fetch(context.Background(), srv.URL+"/files/a%20b")
	if err != nil || string(data) != body || gotAuth != "Bearer "+realToken || gotPath != "/files/a%20b" {
		t.Fatalf("Fetch = %q, %v, auth %q, path %q", data, err, gotAuth, gotPath)
	}

	status = http.StatusUnauthorized
	if _, err := f.Fetch(context.Background(), srv.URL+"/files/x"); err == nil || !strings.Contains(err.Error(), "401") || resolverCause(err) != "http-401" {
		t.Fatalf("401 = %v", err)
	}
	if len(tokens.invalidated) != 1 || tokens.invalidated[0] != realToken {
		t.Fatalf("invalidated = %v", tokens.invalidated)
	}

	status, body = http.StatusOK, strings.Repeat("a", maxResolverBody+1)
	if _, err := f.Fetch(context.Background(), srv.URL+"/files/x"); err == nil || !strings.Contains(err.Error(), "too large") || resolverCause(err) != "bad-response" {
		t.Fatalf("oversized = %v", err)
	}

	tokens.err = &token.RefreshError{Status: http.StatusBadRequest, Code: "invalid_grant"}
	if _, err := f.Fetch(context.Background(), srv.URL+"/files/x"); err == nil || resolverCause(err) != "token:invalid_grant" {
		t.Fatalf("Fetch without a token = %v (%s)", err, resolverCause(err))
	}
	tokens.err = nil
	srv.Close()
	if _, err := f.Fetch(context.Background(), srv.URL+"/files/x"); err == nil || resolverCause(err) != "unreachable" {
		t.Fatalf("Fetch against a closed server = %v", err)
	}
}

func TestResolverWithProtobufKeys(t *testing.T) {
	f := &staticFetcher{body: `{"parent_ids":["FOLDER1"]}`}
	extract, _ := filter.ParseJSONPath("$.parentIds[*]")
	r := &resolver{
		name: "p", def: filter.Resolver{URL: "https://x/{id}", Extract: "$.parentIds[*]", MaxDepth: 1},
		extract: extract.WithProtobufNames(), fetch: f, cache: newParentCache(time.Now), jsonKeys: filter.JSONKeysProtobuf,
	}
	if ok, err := r.within(context.Background(), "FILE", map[string]bool{"FOLDER1": true}); !ok || err != nil {
		t.Fatalf("within = %v, %v", ok, err)
	}
	f.body = `{"parents":{"id":"x"}}`
	r.extract, _ = filter.ParseJSONPath("$.parents")
	r.cache = newParentCache(time.Now)
	if _, err := r.within(context.Background(), "FILE", map[string]bool{"FOLDER1": true}); err == nil {
		t.Fatal("object leaf accepted")
	}
}

type staticFetcher struct{ body string }

func (s *staticFetcher) Fetch(context.Context, string) ([]byte, error) { return []byte(s.body), nil }
