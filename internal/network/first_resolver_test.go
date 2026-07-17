// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package network

import "testing"

func TestFirstIPv4Resolver(t *testing.T) {
	cases := []struct {
		name       string
		candidates []string
		want       string
	}{
		{"skips IPv6-only candidates", []string{"2001:4860:4860::8888", "1.1.1.1"}, "1.1.1.1"},
		{"skips invalid entries", []string{"not-an-ip", " ", "8.8.4.4"}, "8.8.4.4"},
		{"empty falls back to default", nil, DefaultResolver},
		{"all invalid falls back to default", []string{"::1", "garbage"}, DefaultResolver},
		{"trims whitespace", []string{"  9.9.9.9 "}, "9.9.9.9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FirstIPv4Resolver(tc.candidates); got != tc.want {
				t.Fatalf("FirstIPv4Resolver(%v) = %q, want %q", tc.candidates, got, tc.want)
			}
		})
	}
}
