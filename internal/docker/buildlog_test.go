// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"bytes"
	"strings"
	"testing"
)

func TestBuildLogForwardsAndKeepsTail(t *testing.T) {
	var forwarded bytes.Buffer
	log := NewBuildLog(&forwarded)
	if _, err := log.Write([]byte("step 1\nstep 2\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if forwarded.String() != "step 1\nstep 2\n" {
		t.Fatalf("forwarded output = %q", forwarded.String())
	}
	if log.Tail() != "step 1\nstep 2\n" {
		t.Fatalf("tail = %q", log.Tail())
	}
}

func TestBuildLogTailIsBounded(t *testing.T) {
	log := NewBuildLog(nil)
	log.max = 16
	for i := 0; i < 10; i++ {
		if _, err := log.Write([]byte("0123456789")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	tail := log.Tail()
	if len(tail) != 16 || !strings.HasSuffix(tail, "0123456789") {
		t.Fatalf("expected the last 16 bytes, got %q", tail)
	}
	// A single write larger than the bound keeps its last bytes.
	if _, err := log.Write([]byte(strings.Repeat("a", 30) + "END")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if tail := log.Tail(); len(tail) != 16 || !strings.HasSuffix(tail, "END") {
		t.Fatalf("expected the last 16 bytes of the large write, got %q", tail)
	}
}
