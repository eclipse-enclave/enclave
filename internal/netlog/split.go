// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import "bytes"

// maxLineBytes bounds a single log line. Paths are the only unbounded field and
// a URL path cannot reach this size in practice.
const maxLineBytes = 1024 * 1024

// readChunkBytes is how much is read from a log at a time.
const readChunkBytes = 32 * 1024

// lineSplitter turns a byte stream into lines. Scan and the tail both feed it,
// so the bound on a single line and the resynchronization after an oversized one
// are defined once.
type lineSplitter struct {
	partial []byte
	// dropping is set while an oversized line is discarded, until the newline
	// that ends it.
	dropping bool
}

// feed splits chunk into lines, holding back a trailing partial line until the
// writer finishes it. A line longer than maxLineBytes is discarded and drop is
// called once for it: without that bound a torn or interleaved write would grow
// the buffer indefinitely in a follower that runs for a whole session. Emitted
// lines alias the chunk or the splitter's own buffer, so they are only valid
// until the next call.
func (s *lineSplitter) feed(chunk []byte, emit func(line []byte), drop func()) {
	// Only a line that spans the chunk boundary has to be assembled.
	buffer := chunk
	if len(s.partial) > 0 {
		s.partial = append(s.partial, chunk...)
		buffer = s.partial
	}

	if s.dropping {
		// Resynchronize: everything up to the next newline belongs to the line
		// that was discarded.
		index := bytes.IndexByte(buffer, '\n')
		if index < 0 {
			s.partial = s.partial[:0]
			return
		}
		s.dropping = false
		buffer = buffer[index+1:]
	}

	for {
		index := bytes.IndexByte(buffer, '\n')
		if index < 0 {
			break
		}
		line := buffer[:index]
		buffer = buffer[index+1:]
		if len(line) > maxLineBytes {
			// Complete, so there is nothing to resynchronize past.
			reportDrop(drop)
			continue
		}
		emit(line)
	}

	if len(buffer) > maxLineBytes {
		// No newline in sight: stop holding the line and pick up at the next one.
		s.dropping = true
		reportDrop(drop)
		buffer = nil
	}
	// Retain the unterminated remainder.
	s.partial = append(s.partial[:0], buffer...)
}

func reportDrop(drop func()) {
	if drop != nil {
		drop()
	}
}

// flush emits a trailing line that no newline ever terminated. Only a reader
// that has reached the end of a finite stream may call it; a tail must not,
// because its writer may still be mid-line.
func (s *lineSplitter) flush(emit func(line []byte)) {
	line, dropping := s.partial, s.dropping
	s.reset()
	if dropping || len(line) == 0 {
		return
	}
	emit(line)
}

func (s *lineSplitter) reset() {
	s.partial = nil
	s.dropping = false
}
