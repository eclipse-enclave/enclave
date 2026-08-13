// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// maxLineBytes bounds a single log line. Paths are the only unbounded field and
// a URL path cannot reach this size in practice.
const maxLineBytes = 1024 * 1024

// ScanResult holds the events read from a stream plus the number of lines that
// could not be parsed. Older logs contain raw dnsmasq text appended by the
// gateway shell, so a non-zero Skipped is expected rather than fatal.
type ScanResult struct {
	Events  []Event
	Skipped int
}

// Scan reads JSONL events until EOF. Blank lines are ignored; every other
// unparseable line increments Skipped.
func Scan(reader io.Reader) (ScanResult, error) {
	var result ScanResult
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for scanner.Scan() {
		event, ok := ParseLine(scanner.Bytes())
		if !ok {
			if strings.TrimSpace(scanner.Text()) != "" {
				result.Skipped++
			}
			continue
		}
		result.Events = append(result.Events, event)
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	return result, nil
}

// ParseLine decodes one JSONL line. The second return is false when the line is
// not a well-formed event.
func ParseLine(line []byte) (Event, bool) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return Event{}, false
	}
	var event Event
	if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
		return Event{}, false
	}
	if strings.TrimSpace(event.Type) == "" || strings.TrimSpace(event.Timestamp) == "" {
		return Event{}, false
	}
	return event, true
}
