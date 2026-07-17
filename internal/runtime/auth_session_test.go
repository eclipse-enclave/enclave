// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import "testing"

func TestJSONPointerExists(t *testing.T) {
	payload := map[string]any{
		"openai-codex": map[string]any{
			"access": "token",
		},
		"nested": map[string]any{
			"list": []any{
				map[string]any{"value": "ok"},
			},
		},
		"a/b":  "slash",
		"a~b":  "tilde",
		"null": nil,
	}

	cases := []struct {
		name    string
		pointer string
		want    bool
	}{
		{"root", "", true},
		{"top_level", "/openai-codex", true},
		{"missing", "/missing", false},
		{"nested_value", "/nested/list/0/value", true},
		{"escaped_slash", "/a~1b", true},
		{"escaped_tilde", "/a~0b", true},
		{"null_value", "/null", true},
		{"invalid", "openai-codex", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonPointerExists(payload, tc.pointer); got != tc.want {
				t.Fatalf("jsonPointerExists(%q) = %v, want %v", tc.pointer, got, tc.want)
			}
		})
	}
}

func TestJSONPointerNonNull(t *testing.T) {
	payload := map[string]any{
		"present": "value",
		"null":    nil,
	}

	cases := []struct {
		name    string
		pointer string
		want    bool
	}{
		{"present", "/present", true},
		{"null_value", "/null", false},
		{"missing", "/missing", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonPointerNonNull(payload, tc.pointer); got != tc.want {
				t.Fatalf("jsonPointerNonNull(%q) = %v, want %v", tc.pointer, got, tc.want)
			}
		})
	}
}
