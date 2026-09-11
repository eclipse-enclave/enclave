// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// ScanResult holds the events read from a stream plus the number of lines that
// could not be parsed. Older logs contain raw dnsmasq text appended by the
// gateway shell, so a non-zero Skipped is expected rather than fatal.
type ScanResult struct {
	Events  []Event
	Skipped int
	// Offset is how far into the stream the scan got, excluding a final line that
	// no newline terminated: a writer mid-append leaves one, and a caller that
	// goes on to follow the same file has to start before it to read it whole.
	// Pass it as a FollowOptions.StartOffset to continue exactly here.
	Offset int64
}

// Scan reads JSONL events until EOF. Blank lines are ignored; every other
// unparseable line increments Skipped, including a line above maxLineBytes.
// Several processes append to the log concurrently, so a torn line must not make
// the rest of the history unreadable.
func Scan(reader io.Reader) (ScanResult, error) {
	var result ScanResult
	var splitter lineSplitter

	consume := func(line []byte) {
		if event, ok := ParseLine(line); ok {
			result.Events = append(result.Events, event)
			return
		}
		if len(bytes.TrimSpace(line)) > 0 {
			result.Skipped++
		}
	}
	drop := func() { result.Skipped++ }

	buffer := make([]byte, readChunkBytes)
	for {
		read, err := reader.Read(buffer)
		if read > 0 {
			result.Offset += int64(read)
			splitter.feed(buffer[:read], consume, drop)
		}
		if err != nil {
			splitter.flush(func(line []byte) {
				result.Offset -= int64(len(line))
				consume(line)
			})
			if errors.Is(err, io.EOF) {
				return result, nil
			}
			return result, err
		}
	}
}

// ParseLine decodes one JSONL line. The second return is false when the line is
// not a well-formed event.
func ParseLine(line []byte) (Event, bool) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Event{}, false
	}
	var event Event
	if err := json.Unmarshal(trimmed, &event); err != nil {
		return Event{}, false
	}
	if strings.TrimSpace(event.Type) == "" || strings.TrimSpace(event.Timestamp) == "" {
		return Event{}, false
	}
	return event, true
}
