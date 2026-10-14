// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package backend

import (
	"slices"
	"testing"
)

func TestContainsSecretPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name, data   string
		placeholders []string
		want         bool
	}{
		{"ordinary credentials", `{"key":"real-key"}`, nil, false},
		{"default from older session", `{"key":"ENCLAVE_SECRET_old"}`, []string{"ENCLAVE_SECRET_new"}, true},
		{"prefixed OAuth", `{"key":"sk-ant-oat01-ENCLAVE_SECRET_old"}`, nil, true},
		{"prefixed PAT", `{"key":"at-ENCLAVE_SECRET_old"}`, nil, true},
		{"custom current session", `{"key":"vendor_random_end"}`, []string{"other_placeholder", "vendor_random_end"}, true},
		{"literal custom shape", "prefix.a[b]*suffix", []string{"prefix.a[b]*suffix"}, true},
		{"empty placeholder ignored", `{"key":"real-key"}`, []string{""}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContainsSecretPlaceholder([]byte(tc.data), tc.placeholders); got != tc.want {
				t.Fatalf("ContainsSecretPlaceholder() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSecretPlaceholderWireFormat(t *testing.T) {
	for _, tc := range []struct {
		name    string
		values  []string
		wire    string
		decoded []string
	}{
		{"empty", nil, "", nil},
		{"all empty", []string{"", ""}, "", nil},
		{"empty entries omitted", []string{"", "first", "", "second", ""}, "first\nsecond", []string{"first", "second"}},
		{"literal characters", []string{"prefix.a[b]*suffix", "vendor-'quoted'"}, "prefix.a[b]*suffix\nvendor-'quoted'", []string{"prefix.a[b]*suffix", "vendor-'quoted'"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.values)
			if wire := EncodeSecretPlaceholders(tc.values); wire != tc.wire {
				t.Fatalf("wire = %q, want %q", wire, tc.wire)
			}
			if !slices.Equal(tc.values, before) {
				t.Fatal("encoding mutated caller's list")
			}
			for _, wire := range []string{tc.wire, "\n" + tc.wire + "\n\n"} {
				if got := DecodeSecretPlaceholders(wire); !slices.Equal(got, tc.decoded) {
					t.Fatalf("decoded %q = %v, want %v", wire, got, tc.decoded)
				}
			}
		})
	}
}

func TestSecretPlaceholders(t *testing.T) {
	got := SecretPlaceholders([]SecretRelease{{Placeholder: "fake-a", Value: "real-a"}, {}, {Placeholder: "fake-b", Value: "real-b"}})
	if len(got) != 2 || got[0] != "fake-a" || got[1] != "fake-b" {
		t.Fatalf("placeholders = %v", got)
	}
}
