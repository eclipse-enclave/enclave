// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"testing"
)

func TestParseManagedName(t *testing.T) {
	tool, hash, tail, ok := ParseManagedName("enclave-open-code-abc123abc123-feature-1")
	if !ok {
		t.Fatal("expected managed name to parse")
	}
	if tool != "open-code" {
		t.Fatalf("expected tool open-code, got %q", tool)
	}
	if hash != "abc123abc123" {
		t.Fatalf("expected hash abc123abc123, got %q", hash)
	}
	if tail != "feature-1" {
		t.Fatalf("expected tail feature-1, got %q", tail)
	}
}

func TestPrimaryContainerName(t *testing.T) {
	tests := []struct {
		name    string
		summary Summary
		want    string
	}{
		{name: "docker name", summary: Summary{Names: []string{"/demo"}}, want: "demo"},
		{name: "id fallback", summary: Summary{ID: "1234567890abcdef"}, want: "1234567890ab"},
		{name: "raw short id", summary: Summary{ID: "shortid"}, want: "shortid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PrimaryContainerName(tt.summary); got != tt.want {
				t.Fatalf("PrimaryContainerName() = %q, want %q", got, tt.want)
			}
		})
	}
}
