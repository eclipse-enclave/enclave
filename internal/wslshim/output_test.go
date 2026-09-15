// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

import (
	"reflect"
	"testing"
)

// utf16LE builds the byte fixtures wsl.exe actually produces, so the tests do
// not depend on the decoder they are testing.
func utf16LE(s string, withBOM bool) []byte {
	var out []byte
	if withBOM {
		out = append(out, 0xFF, 0xFE)
	}
	for _, r := range s {
		// Every character in these fixtures is inside the BMP.
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

func TestDecodeWSLOutputUTF16LE(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{"with BOM", utf16LE("Ubuntu\r\n", true), "Ubuntu\r\n"},
		{"without BOM", utf16LE("Ubuntu\r\n", false), "Ubuntu\r\n"},
		{"error message", utf16LE("There is no distribution with the supplied name.\r\n", false),
			"There is no distribution with the supplied name.\r\n"},
		{"non-ascii", utf16LE("Ubuntu-24.04 läuft\r\n", false), "Ubuntu-24.04 läuft\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeWSLOutput(tc.raw); got != tc.want {
				t.Errorf("decodeWSLOutput = %q, want %q", got, tc.want)
			}
		})
	}
}

// Output relayed from a Linux process is not re-encoded, so UTF-8 must pass
// through untouched.
func TestDecodeWSLOutputPassesUTF8Through(t *testing.T) {
	cases := map[string]string{
		"/home/p/.local/bin/enclave\n": "/home/p/.local/bin/enclave\n",
		"":                             "",
		"/":                            "/",
		"ünïcödé path 🚀":               "ünïcödé path 🚀",
		"\ufeff/usr/bin/enclave":       "/usr/bin/enclave",
	}
	for raw, want := range cases {
		if got := decodeWSLOutput([]byte(raw)); got != want {
			t.Errorf("decodeWSLOutput(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestDecodeWSLOutputStripsUTF16BOM(t *testing.T) {
	if got := decodeWSLOutput(utf16LE("\ufeffUbuntu", false)); got != "Ubuntu" {
		t.Errorf("decodeWSLOutput = %q, want %q", got, "Ubuntu")
	}
}

func TestIsUTF16LEDoesNotMisreadUTF8(t *testing.T) {
	// A lone NUL in otherwise-UTF-8 output must not flip the detection.
	if isUTF16LE([]byte("/usr/bin/enclave\x00")) {
		t.Error("mostly-UTF-8 output was detected as UTF-16LE")
	}
	if isUTF16LE([]byte("a")) {
		t.Error("a single byte cannot be UTF-16LE")
	}
	if isUTF16LE(nil) {
		t.Error("empty output cannot be UTF-16LE")
	}
}

func TestParseDistroList(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want []string
	}{
		{
			name: "utf-16 with BOM as wsl --list --quiet emits it",
			raw:  utf16LE("Ubuntu-24.04\r\nDebian\r\ndocker-desktop\r\n", true),
			want: []string{"Ubuntu-24.04", "Debian", "docker-desktop"},
		},
		{
			name: "utf-16 without BOM",
			raw:  utf16LE("Ubuntu\r\n", false),
			want: []string{"Ubuntu"},
		},
		{
			name: "blank lines are dropped",
			raw:  utf16LE("Ubuntu\r\n\r\n\r\nDebian\r\n", false),
			want: []string{"Ubuntu", "Debian"},
		},
		{
			name: "no distributions installed",
			raw:  utf16LE("\r\n", false),
			want: nil,
		},
		{
			name: "empty output",
			raw:  nil,
			want: nil,
		},
		{
			name: "utf-8 output still parses",
			raw:  []byte("Ubuntu\nDebian\n"),
			want: []string{"Ubuntu", "Debian"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDistroList(tc.raw); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseDistroList = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCollapseLines(t *testing.T) {
	got := collapseLines("first line\r\nsecond line\r\n")
	if got != "first line; second line" {
		t.Errorf("collapseLines = %q", got)
	}
}
