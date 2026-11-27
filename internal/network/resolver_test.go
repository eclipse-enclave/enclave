// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package network

import "testing"

func TestNormalizeResolverAcceptsIPv4(t *testing.T) {
	for input, want := range map[string]string{
		"1.1.1.1":        "1.1.1.1",
		" 127.0.0.11 ":   "127.0.0.11",
		"::ffff:9.9.9.9": "9.9.9.9",
	} {
		got, err := NormalizeResolver(input)
		if err != nil {
			t.Fatalf("NormalizeResolver(%q) failed: %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeResolver(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeResolverRejectsUnusableAddresses(t *testing.T) {
	for _, input := range []string{
		"",
		"dns.example.com",
		"2606:4700:4700::1111",
		"1.1.1.1:53",
		"0.0.0.0",
		"255.255.255.255",
		"224.0.0.251",
	} {
		if got, err := NormalizeResolver(input); err == nil {
			t.Fatalf("NormalizeResolver(%q) = %q, want error", input, got)
		}
	}
}
