// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"io"
	"sync"
)

// buildLogTailBytes bounds the output a BuildLog retains for failure
// classification. Engine output for a full image build can run to megabytes;
// the cause of a failure is in the last few kilobytes.
const buildLogTailBytes = 64 << 10

// BuildLog forwards build output to a writer (usually the terminal) while
// keeping the tail for failure classification.
type BuildLog struct {
	mu   sync.Mutex
	out  io.Writer
	tail []byte
	max  int
}

// NewBuildLog returns a BuildLog that forwards to out. A nil out discards the
// stream and only keeps the tail.
func NewBuildLog(out io.Writer) *BuildLog {
	if out == nil {
		out = io.Discard
	}
	return &BuildLog{out: out, max: buildLogTailBytes}
}

// Write forwards p and appends it to the retained tail.
func (l *BuildLog) Write(p []byte) (int, error) {
	n, err := l.out.Write(p)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.append(p[:n])
	return n, err
}

func (l *BuildLog) append(p []byte) {
	if len(p) >= l.max {
		l.tail = append(l.tail[:0], p[len(p)-l.max:]...)
		return
	}
	l.tail = append(l.tail, p...)
	if excess := len(l.tail) - l.max; excess > 0 {
		l.tail = append(l.tail[:0], l.tail[excess:]...)
	}
}

// Tail returns the retained end of the output.
func (l *BuildLog) Tail() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.tail)
}
