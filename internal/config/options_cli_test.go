// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"reflect"
	"testing"

	"enclave/internal/model"
	"enclave/internal/util"
)

func TestAppendCSVUnique(t *testing.T) {
	// Dedup against the existing slice and within the input; blanks skipped.
	// `added` counts every kept (non-empty, accepted) item, including ones that
	// were already present — callers use added==0 to detect "no values".
	dst, added, err := appendCSVUnique([]string{"a"}, "a, b , ,c,b", keepCSVValue)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(dst, want) {
		t.Fatalf("dst = %v, want %v", dst, want)
	}
	if added != 4 {
		t.Fatalf("added = %d, want 4 (a,b,c,b; blank skipped)", added)
	}

	// normalize returning keep=false drops the item and does not count it.
	dst2, added2, err := appendCSVUnique(nil, "x,y", func(s string) (string, bool, error) {
		return s, s == "x", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(dst2, []string{"x"}) || added2 != 1 {
		t.Fatalf("keep=false: dst=%v added=%d, want [x] 1", dst2, added2)
	}

	// A validator error propagates.
	if _, _, err := appendCSVUnique(nil, "ok,BAD", func(s string) (string, bool, error) {
		if s == "BAD" {
			return "", false, fmt.Errorf("bad value")
		}
		return s, true, nil
	}); err == nil {
		t.Fatal("expected validator error to propagate")
	}
}

func TestValidateAuthName(t *testing.T) {
	valid := []struct {
		in   string
		want string
	}{
		{"personal", "personal"},
		{"api-key", "api-key"},
		{"api2", "api2"},
		{"Personal", "personal"},         // lowercased
		{"  Api  ", "api"},               // trimmed + lowercased
		{"abcdefghijkl", "abcdefghijkl"}, // 12 chars but not hex -> not hash-like
	}
	for _, tt := range valid {
		t.Run("valid/"+tt.in, func(t *testing.T) {
			got, err := ValidateAuthName(tt.in)
			if err != nil {
				t.Fatalf("ValidateAuthName(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ValidateAuthName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	invalid := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, // 33 chars
		{"underscore", "my_id"},
		{"interior space", "my id"},
		{"leading hyphen", "-personal"},
		{"trailing hyphen", "personal-"},
		{"hash-like slug", "abcdef123456"},    // 12 hex chars
		{"hash-like token", "x-abcdef123456"}, // hash-like segment inside
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			if _, err := ValidateAuthName(tt.in); err == nil {
				t.Fatalf("ValidateAuthName(%q) expected error, got nil", tt.in)
			}
		})
	}
}

func TestProjectHashForPath(t *testing.T) {
	const path = "/some/project/path"
	got := ProjectHashForPath(path)
	if want := model.ShortHash(util.HashString(path)); got != want {
		t.Fatalf("ProjectHashForPath = %q, want %q", got, want)
	}
	if len(got) != model.HashLength {
		t.Fatalf("hash length = %d, want %d", len(got), model.HashLength)
	}
	if ProjectHashForPath(path) != got {
		t.Fatal("ProjectHashForPath is not deterministic")
	}
}
