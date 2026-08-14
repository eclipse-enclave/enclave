// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"strings"
	"testing"
)

// feedAll pushes the input through the splitter in fixed-size chunks, copying
// every emitted line: they alias the splitter's buffer and are only valid for the
// duration of the call.
func feedAll(input string, chunk int, flush bool) (lines []string, dropped int) {
	var splitter lineSplitter
	emit := func(line []byte) { lines = append(lines, string(line)) }
	drop := func() { dropped++ }
	for start := 0; start < len(input); start += chunk {
		end := start + chunk
		if end > len(input) {
			end = len(input)
		}
		splitter.feed([]byte(input[start:end]), emit, drop)
	}
	if flush {
		splitter.flush(emit)
	}
	return lines, dropped
}

func TestLineSplitterReassemblesAcrossChunkSizes(t *testing.T) {
	input := "alpha\nbravo\n\ncharlie\n"
	want := []string{"alpha", "bravo", "", "charlie"}

	// Every chunk size exercises a different set of boundaries, including ones
	// that fall inside a line and immediately before a newline.
	for chunk := 1; chunk <= len(input)+2; chunk++ {
		lines, dropped := feedAll(input, chunk, true)
		if dropped != 0 {
			t.Fatalf("chunk %d: dropped = %d, want 0", chunk, dropped)
		}
		if len(lines) != len(want) {
			t.Fatalf("chunk %d: lines = %q, want %q", chunk, lines, want)
		}
		for i, line := range lines {
			if line != want[i] {
				t.Fatalf("chunk %d: lines = %q, want %q", chunk, lines, want)
			}
		}
	}
}

func TestLineSplitterHoldsAnUnterminatedLineUntilFlush(t *testing.T) {
	lines, _ := feedAll("alpha\nbra", 4, false)
	if len(lines) != 1 || lines[0] != "alpha" {
		t.Fatalf("lines = %q, want only the terminated line", lines)
	}

	flushed, _ := feedAll("alpha\nbra", 4, true)
	if len(flushed) != 2 || flushed[1] != "bra" {
		t.Fatalf("lines = %q, want the remainder on flush", flushed)
	}
}

func TestLineSplitterDropsAnOversizedLineOnceAndResynchronizes(t *testing.T) {
	huge := strings.Repeat("x", 3*maxLineBytes)

	// Terminated: the line is complete, so the next one follows directly.
	lines, dropped := feedAll(huge+"\nafter\n", readChunkBytes, true)
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	if len(lines) != 1 || lines[0] != "after" {
		t.Fatalf("lines = %q, want only the line after the dropped one", lines)
	}

	// Unterminated: everything up to the next newline is that line's tail, so the
	// first newline resynchronizes and the line after it is emitted.
	lines, dropped = feedAll(huge+"tail\nafter\n", readChunkBytes, true)
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	if len(lines) != 1 || lines[0] != "after" {
		t.Fatalf("lines = %q, want only the line after the resynchronization", lines)
	}
}

func TestLineSplitterFlushDoesNotEmitADiscardedTail(t *testing.T) {
	lines, dropped := feedAll(strings.Repeat("x", 2*maxLineBytes), readChunkBytes, true)
	if dropped != 1 || len(lines) != 0 {
		t.Fatalf("lines = %d and dropped = %d, want none and 1", len(lines), dropped)
	}
}
