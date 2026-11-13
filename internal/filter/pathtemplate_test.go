// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package filter

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePathTemplateRejects(t *testing.T) {
	tests := []struct {
		template string
		want     string
	}{
		{"", "must not be empty"},
		{"gmail/v1", "must start with"},
		{"/a//b", "empty segment"},
		{"/a/./b", "dot segments"},
		{"/a/../b", "dot segments"},
		{"/a/%2F", "must not contain"},
		{"/a/b c", "must not contain"},
		{"/a/x?y", "must not contain"},
		{"/a/b*", "must not contain"},
		{"/a/{", "unbalanced"},
		{"/a/}", "unbalanced"},
		{"/a/{1x}", "invalid variable name"},
		{"/a/{id}/{id}", "twice"},
		{"/a/{id:}", "empty pattern"},
		{"/a/{id:[}", "invalid pattern"},
		{"/a/{id:a/b}", "must not contain \"/\""},
		{"/a/{id}x", "must not start with"},
		{"/a/{id}_x", "must not start with"},
		{"/a/{id}:{op}", "must not contain"},
		{"/a/pre{id}", "must not contain"},
		{"/**/a", "final segment"},
		{"/a/**/", "final segment"},
		{"/a\x01", "control characters"},
		{"/" + strings.Repeat("a", maxTemplateLength), "longer than"},
	}
	for _, tt := range tests {
		t.Run(tt.template, func(t *testing.T) {
			_, err := ParsePathTemplate(tt.template)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParsePathTemplate(%q) error = %v, want %q", tt.template, err, tt.want)
			}
		})
	}
}

func TestPathTemplateMatch(t *testing.T) {
	tests := []struct {
		name     string
		template string
		path     string
		want     map[string]string
	}{
		{"literal", "/gmail/v1/users/me/profile", "/gmail/v1/users/me/profile", map[string]string{}},
		{"literal is case sensitive", "/gmail/v1", "/Gmail/v1", nil},
		{"capture", "/messages/{id}", "/messages/18c2a", map[string]string{"id": "18c2a"}},
		{"capture rejects colon", "/messages/{id}", "/messages/a:b", nil},
		{"capture rejects dot", "/messages/{id}", "/messages/a.b", nil},
		{"capture decodes once", "/files/{id}", "/files/a%2Db", map[string]string{"id": "a-b"}},
		{"capture rejects decoded space", "/files/{id}", "/files/a%20b", nil},
		{"capture with suffix", "/v1/documents/{documentId}:batchUpdate", "/v1/documents/1AbC_-x:batchUpdate", map[string]string{"documentId": "1AbC_-x"}},
		{"suffix mismatch", "/v1/documents/{documentId}:batchUpdate", "/v1/documents/1AbC:get", nil},
		{"suffix without capture", "/v1/documents/{documentId}:batchUpdate", "/v1/documents/:batchUpdate", nil},
		{"custom regex", "/v/{n:[0-9]{3}}", "/v/123", map[string]string{"n": "123"}},
		{"custom regex anchored", "/v/{n:[0-9]{3}}", "/v/1234", nil},
		{"custom regex with suffix", "/v/{n:[a-z.]+}.json", "/v/a.b.json", map[string]string{"n": "a.b"}},
		{"rest matches many", "/files/**", "/files/a/b/c", map[string]string{}},
		{"rest needs one segment", "/files/**", "/files", nil},
		{"rest rejects trailing slash", "/files/**", "/files/a/", nil},
		{"trailing slash required", "/files/", "/files/", map[string]string{}},
		{"trailing slash not matched", "/files", "/files/", nil},
		{"trailing slash missing", "/files/", "/files", nil},
		{"root", "/", "/", map[string]string{}},
		{"too long", "/a/{id}", "/a/b/c", nil},
		{"too short", "/a/{id}/c", "/a/b", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl, err := ParsePathTemplate(tt.template)
			if err != nil {
				t.Fatalf("ParsePathTemplate: %v", err)
			}
			p, err := ParseRequestPath(tt.path)
			if err != nil {
				t.Fatalf("ParseRequestPath(%q): %v", tt.path, err)
			}
			got, ok := tpl.Match(p)
			if tt.want == nil {
				if ok {
					t.Fatalf("Match(%q) = %v, want no match", tt.path, got)
				}
				return
			}
			if !ok || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Match(%q) = %v, %v, want %v", tt.path, got, ok, tt.want)
			}
		})
	}
}

// I5: every ambiguous path form is rejected before matching.
func TestParseRequestPathRejectsAmbiguousForms(t *testing.T) {
	tests := []string{
		"",
		"relative",
		"/a/%2F/b",
		"/a%2fb",
		"/a/%5C",
		"/a/%5cb",
		"/a/%00",
		"/a\\b",
		"/a/./b",
		"/a/../b",
		"/a/%2e%2e/b",
		"/a/%2E/b",
		"/a/.%2e",
		"/a//b",
		"//a",
		"/a/%zz",
		"/a/%0a",
		"/a/%7f",
	}
	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			if got, err := ParseRequestPath(path); err == nil {
				t.Fatalf("ParseRequestPath(%q) = %+v, want error", path, got)
			}
		})
	}
}

func TestParseRequestPathSegments(t *testing.T) {
	got, err := ParseRequestPath("/a/b%20c/d/")
	if err != nil {
		t.Fatal(err)
	}
	want := RequestPath{Segments: []string{"a", "b c", "d"}, TrailingSlash: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if tpl, _ := ParsePathTemplate("/a/{x}/d/"); tpl.String() != "/a/{x}/d/" || !reflect.DeepEqual(tpl.Captures(), []string{"x"}) {
		t.Fatalf("template accessors wrong: %q %v", tpl.String(), tpl.Captures())
	}
}
