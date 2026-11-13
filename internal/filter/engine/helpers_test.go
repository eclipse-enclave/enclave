// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"enclave/internal/filter"
	"enclave/internal/filter/spec"
)

const (
	testPlaceholder = "ENCLAVE_SECRET_0123456789abcdef0123456789abcdef"
	realToken       = "ya29.real-access-token-never-in-container"
	fixtureDir      = "../testdata/gws"
)

type fakeTokens struct {
	mu          sync.Mutex
	value       string
	err         error
	calls       int
	invalidated []string
}

func (f *fakeTokens) Token(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.value, nil
}

func (f *fakeTokens) Invalidate(value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated = append(f.invalidated, value)
}

// fakeFetcher serves resolver lookups from a parent table. IDs in failing
// produce errors.
type fakeFetcher struct {
	mu      sync.Mutex
	parents map[string][]string
	failing map[string]bool
	calls   map[string]int
	urls    []string
}

func newFakeFetcher() *fakeFetcher {
	return &fakeFetcher{
		parents: map[string][]string{
			"FILE_IN":   {"FOLDER1"},
			"FILE_DEEP": {"SUB"},
			"SUB":       {"FOLDER1"},
			"FILE_OUT":  {"OTHER"},
			"OTHER":     {"ROOT"},
			"ROOT":      {},
			"FILE_UP":   {"P_ERR"},
			"CYCLE_A":   {"CYCLE_B"},
			"CYCLE_B":   {"CYCLE_A"},
			"SUB_OUT":   {"OTHER"},
		},
		failing: map[string]bool{"FILE_ERR": true, "P_ERR": true},
		calls:   map[string]int{},
	}
}

func (f *fakeFetcher) Fetch(_ context.Context, rawURL string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, rawURL)
	const prefix = "https://www.googleapis.com/drive/v3/files/"
	id, _, _ := strings.Cut(strings.TrimPrefix(rawURL, prefix), "?")
	f.calls[id]++
	if f.failing[id] {
		return nil, lookupFailure("unreachable", errors.New("lookup failed"))
	}
	parents, ok := f.parents[id]
	if !ok {
		return nil, lookupFailure("http-404", errors.New("not found"))
	}
	data, _ := json.Marshal(map[string]any{"parents": parents})
	return data, nil
}

type harness struct {
	engine  *Engine
	tokens  *fakeTokens
	fetcher *fakeFetcher
}

// newHarness compiles the fixture with a preset. Extra presets are written to
// a user preset directory first.
func newHarness(t *testing.T, preset string, userPresets map[string]string) *harness {
	t.Helper()
	f, err := spec.Load(fixtureDir, "gws")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	userDir := t.TempDir()
	for name, body := range userPresets {
		if err := os.WriteFile(filepath.Join(userDir, name+".yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := f.ResolvePreset(preset, userDir)
	if err != nil {
		t.Fatalf("ResolvePreset: %v", err)
	}
	pol, err := spec.Compile(f, p, testPlaceholder)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return newHarnessForPolicy(t, pol)
}

func newHarnessForPolicy(t *testing.T, pol *filter.Policy) *harness {
	t.Helper()
	h := &harness{tokens: &fakeTokens{value: realToken}, fetcher: newFakeFetcher()}
	e, err := New(Options{Policy: pol, Tokens: h.tokens, Fetcher: h.fetcher})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.engine = e
	return h
}

// parseSpecPolicy compiles an inline spec with an inline preset.
func parseSpecPolicy(t *testing.T, specYAML string, preset *spec.Preset) *filter.Policy {
	t.Helper()
	f, err := spec.Parse([]byte(specYAML), "inline/spec.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if preset.Name == "" {
		preset.Name = "inline"
	}
	pol, err := spec.Compile(f, preset, testPlaceholder)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return pol
}

type reqOption func(*http.Request)

func withHeader(name, value string) reqOption {
	return func(r *http.Request) { r.Header.Add(name, value) }
}

func withoutAuth() reqOption {
	return func(r *http.Request) { r.Header.Del("Authorization") }
}

func withContentType(value string) reqOption { return withHeader("Content-Type", value) }

// withoutContentType keeps newRequest from adding its default Content-Type.
func withoutContentType() reqOption {
	return func(r *http.Request) { r.Header["Content-Type"] = nil }
}

// newRequest builds a request as the gateway receives it, carrying the
// placeholder like the gws client does. A request with a body gets
// "Content-Type: application/json" unless an option sets one.
func newRequest(method, target, body string, opts ...reqOption) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Authorization", "Bearer "+testPlaceholder)
	for _, opt := range opts {
		opt(req)
	}
	if _, set := req.Header["Content-Type"]; body != "" && !set {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func (h *harness) eval(t *testing.T, req *http.Request) *Decision {
	t.Helper()
	return h.engine.Evaluate(context.Background(), "https", req)
}

func mustAllow(t *testing.T, d *Decision) {
	t.Helper()
	if d.Denial != nil {
		t.Fatalf("denied: %s (%s)", d.Denial.Message, d.Denial.Reason)
	}
}

func mustDeny(t *testing.T, d *Decision, reason string, contains string) *Denial {
	t.Helper()
	if d.Denial == nil {
		t.Fatalf("allowed, want deny %s", reason)
	}
	if d.Denial.Reason != reason {
		t.Fatalf("reason = %s (%s), want %s", d.Denial.Reason, d.Denial.Message, reason)
	}
	if !strings.Contains(d.Denial.Message, contains) {
		t.Fatalf("message = %q, want %q", d.Denial.Message, contains)
	}
	return d.Denial
}

func rawMessage(headers string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(headers + "\r\n\r\nbody\r\n"))
}

func sendBody(headers string) string {
	return fmt.Sprintf(`{"raw":%q}`, rawMessage(headers))
}

func multipartBody(boundary string, parts ...[2]string) string {
	var b strings.Builder
	for _, p := range parts {
		fmt.Fprintf(&b, "--%s\r\n%s\r\n\r\n%s\r\n", boundary, p[0], p[1])
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.String()
}
