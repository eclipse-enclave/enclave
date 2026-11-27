// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "testing"

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

func TestParseManagedNameRejectsUnmanagedNames(t *testing.T) {
	for _, name := range []string{
		"",
		"claude-abc123abc123-1",
		"enclave-claude",
		"enclave-claude-1",
		"enclave-abc123abc123-1",
	} {
		if _, _, _, ok := ParseManagedName(name); ok {
			t.Errorf("ParseManagedName(%q) ok, want false", name)
		}
	}
}

func TestParseManagedNameWithoutSessionTail(t *testing.T) {
	tool, hash, tail, ok := ParseManagedName("enclave-claude-abc123abc123")
	if !ok || tool != "claude" || hash != "abc123abc123" || tail != "" {
		t.Fatalf("got (%q, %q, %q, %v)", tool, hash, tail, ok)
	}
}
