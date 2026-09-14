// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

// buildLogTailBytes bounds the output a BuildLog retains for failure
// classification. Engine output for a full image build can run to megabytes;
// the cause of a failure is in the last few kilobytes.
const buildLogTailBytes = 64 << 10

// buildLogLineBytes bounds the partial line kept between writes.
const buildLogLineBytes = 4 << 10

// ansiEscapePattern matches terminal control sequences in progress output so
// the last line reported by a stall warning is plain text.
var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[()][A-Za-z0-9]|\r`)

// BuildLog forwards build output to a writer (usually the terminal) while
// keeping the tail for failure classification and tracking when output last
// arrived so a silent build can be reported.
type BuildLog struct {
	mu       sync.Mutex
	out      io.Writer
	tail     []byte
	max      int
	partial  []byte
	lastLine string
	lastAt   time.Time
	now      func() time.Time
	// poll overrides how often WarnWhenStalled samples Idle (tests).
	poll time.Duration
}

// NewBuildLog returns a BuildLog that forwards to out. A nil out discards the
// stream and only keeps the tail.
func NewBuildLog(out io.Writer) *BuildLog {
	if out == nil {
		out = io.Discard
	}
	now := time.Now
	return &BuildLog{out: out, max: buildLogTailBytes, now: now, lastAt: now()}
}

// Write forwards p and appends it to the retained tail.
func (l *BuildLog) Write(p []byte) (int, error) {
	n, err := l.out.Write(p)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.append(p[:n])
	l.trackLines(p[:n])
	l.lastAt = l.now()
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

func (l *BuildLog) trackLines(p []byte) {
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx < 0 {
			l.partial = append(l.partial, p...)
			if len(l.partial) > buildLogLineBytes {
				l.partial = l.partial[len(l.partial)-buildLogLineBytes:]
			}
			return
		}
		line := append(l.partial, p[:idx]...)
		l.partial = l.partial[:0]
		p = p[idx+1:]
		if text := strings.TrimSpace(ansiEscapePattern.ReplaceAllString(string(line), "")); text != "" {
			l.lastLine = text
		}
	}
}

// Tail returns the retained end of the output.
func (l *BuildLog) Tail() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.tail)
}

// LastLine returns the most recent complete non-empty line, with terminal
// control sequences removed.
func (l *BuildLog) LastLine() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastLine
}

// Idle returns how long ago output last arrived (or the log was created).
func (l *BuildLog) Idle() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.now().Sub(l.lastAt)
}

// WarnWhenStalled calls warn once the log has received no output for the
// given duration, and again for each further such period while the silence
// lasts, passing the idle time and the last line seen. The returned function
// stops the watcher; callers defer it around the build.
func (l *BuildLog) WarnWhenStalled(after time.Duration, warn func(idle time.Duration, lastLine string)) (stop func()) {
	if after <= 0 || warn == nil {
		return func() {}
	}
	poll := l.poll
	if poll <= 0 {
		poll = after / 4
	}
	if poll < 10*time.Millisecond {
		poll = 10 * time.Millisecond
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		var warnedAt time.Duration
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				idle := l.Idle()
				if idle < after {
					warnedAt = 0
					continue
				}
				if idle-warnedAt < after {
					continue
				}
				warnedAt = idle
				warn(idle, l.LastLine())
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}
