// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package domainpattern

import "testing"

func TestNormalizeAcceptsValidPatterns(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "   ", want: ""},
		{name: "exact", input: "API.EXAMPLE.COM", want: "api.example.com"},
		{name: "wildcard", input: "*.Example.com", want: "*.example.com"},
		{name: "single label", input: "localhost", want: "localhost"},
		{name: "trailing dot", input: "api.example.com.", want: "api.example.com"},
		{name: "max label length", input: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.example.com", want: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Normalize(tt.input)
			if err != nil {
				t.Fatalf("Normalize(%q) error = %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeRejectsBroadOrMalformedPatterns(t *testing.T) {
	tests := []string{
		".example.com",
		"foo.*.example.com",
		"*",
		"*.com",
		"example.com:443",
		"https://example.com",
		"example.com/path",
		"*example.com",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.example.com",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := Normalize(input); err == nil {
				t.Fatalf("Normalize(%q) error = nil, want non-nil", input)
			}
		})
	}
}

func TestNormalizeHostAcceptsRuntimeHostForms(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain host", input: "API.EXAMPLE.COM", want: "api.example.com"},
		{name: "leading and trailing dots", input: ".API.EXAMPLE.COM.", want: "api.example.com"},
		{name: "host with port", input: "api.example.com:443", want: "api.example.com"},
		{name: "bracketed ipv6", input: "[2001:db8::1]", want: "2001:db8::1"},
		{name: "bracketed ipv6 with port", input: "[2001:db8::1]:443", want: "2001:db8::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeHost(tt.input)
			if err != nil {
				t.Fatalf("NormalizeHost(%q) error = %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeHost(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeHostRejectsEmptyInput(t *testing.T) {
	if _, err := NormalizeHost("   "); err == nil {
		t.Fatalf("NormalizeHost(empty) error = nil, want non-nil")
	}
}

func TestMatchNormalizedHost(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		pattern string
		want    bool
	}{
		{name: "exact", host: "api.example.com", pattern: "api.example.com", want: true},
		{name: "subdomain", host: "foo.api.example.com", pattern: "api.example.com", want: true},
		{name: "wildcard", host: "foo.example.com", pattern: "*.example.com", want: true},
		{name: "wildcard no apex", host: "example.com", pattern: "*.example.com", want: false},
		{name: "empty host", host: "", pattern: "example.com", want: false},
		{name: "empty pattern", host: "example.com", pattern: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchNormalizedHost(tt.host, tt.pattern); got != tt.want {
				t.Fatalf("MatchNormalizedHost(%q, %q) = %v, want %v", tt.host, tt.pattern, got, tt.want)
			}
		})
	}
}
