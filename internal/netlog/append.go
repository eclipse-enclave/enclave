// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// Appender writes events as JSONL. Several processes append to the same file
// (the MITM proxy and the DNS translator), which is safe because event lines
// are small and O_APPEND writes of that size stay whole on a regular file.
type Appender struct {
	mu   sync.Mutex
	file *os.File
}

// NewAppender opens the log for appending. An empty path yields an appender
// that discards events, so callers need no nil checks.
func NewAppender(path string) (*Appender, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return &Appender{}, nil
	}
	file, err := os.OpenFile(trimmed, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- path comes from trusted runtime wiring.
	if err != nil {
		return nil, err
	}
	return &Appender{file: file}, nil
}

// Append writes one event, stamping the timestamp when the caller left it
// empty. Write errors are dropped: auditing must never break the request path.
func (a *Appender) Append(event Event) {
	if a == nil || a.file == nil {
		return
	}
	if strings.TrimSpace(event.Timestamp) == "" {
		event.Timestamp = time.Now().UTC().Format(TimeFormat)
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	payload = append(payload, '\n')

	a.mu.Lock()
	defer a.mu.Unlock()
	_, _ = a.file.Write(payload)
}

// Close releases the underlying file.
func (a *Appender) Close() error {
	if a == nil || a.file == nil {
		return nil
	}
	return a.file.Close()
}

// AppendEvent opens the log, appends a single event and closes it again. It is
// for one-shot writers such as the host-side session marker.
func AppendEvent(path string, event Event) error {
	appender, err := NewAppender(path)
	if err != nil {
		return err
	}
	appender.Append(event)
	return appender.Close()
}
