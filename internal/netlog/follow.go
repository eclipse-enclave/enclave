// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"bytes"
	"context"
	"io"
	"os"
	"time"
)

// DefaultPollInterval is how often a follower checks a log for growth. Polling
// keeps the follower portable across the platforms enclave supports.
const DefaultPollInterval = 250 * time.Millisecond

// FollowOptions configures a Follower.
type FollowOptions struct {
	// Interval overrides DefaultPollInterval.
	Interval time.Duration
	// FromStart replays the existing contents before waiting for new events.
	FromStart bool
	// StartOffsets overrides where following begins, keyed by path, and takes
	// precedence over FromStart. A caller that already printed the backlog
	// passes the offset its own read stopped at, so nothing is printed twice and
	// nothing appended in between is lost.
	StartOffsets map[string]int64
	// OnSkipped reports lines that could not be parsed or were too long to hold,
	// once per line.
	OnSkipped func()
}

// Follower streams events appended to one or more log files. Files that do not
// exist yet are picked up when they appear, and a rotation (truncation or a new
// inode at the same path) reopens the file from the start.
type Follower struct {
	interval time.Duration
	onSkip   func()
	tails    []*tail
}

// NewFollower builds a follower over the given paths.
func NewFollower(paths []string, opts FollowOptions) *Follower {
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	follower := &Follower{
		interval: interval,
		onSkip:   opts.OnSkipped,
	}
	for _, path := range paths {
		// Without FromStart, history is skipped by remembering how large the
		// log was when the follow began. Recording it here rather than at the
		// first read keeps events appended in between from being lost, and lets
		// a log that only appears later be read from its start.
		var startOffset int64
		if offset, ok := opts.StartOffsets[path]; ok {
			startOffset = offset
		} else if !opts.FromStart {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				startOffset = info.Size()
			}
		}
		follower.tails = append(follower.tails, &tail{path: path, startOffset: startOffset})
	}
	return follower
}

// Run polls until the context is cancelled, calling emit for every new event.
// A cancelled context is a normal stop and returns nil.
func (f *Follower) Run(ctx context.Context, emit func(Event)) error {
	return f.RunLines(ctx, func(line []byte) {
		event, ok := ParseLine(line)
		if !ok {
			if len(bytes.TrimSpace(line)) > 0 && f.onSkip != nil {
				f.onSkip()
			}
			return
		}
		emit(event)
	})
}

// RunLines is Run without the JSONL contract: it hands every appended line to
// the caller. The gateway's DNS translator follows a dnsmasq log this way. The
// line is only valid for the duration of the call: emit must copy what it keeps.
func (f *Follower) RunLines(ctx context.Context, emit func(line []byte)) error {
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()
	defer f.close()

	if err := f.poll(emit); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := f.poll(emit); err != nil {
				return err
			}
		}
	}
}

func (f *Follower) poll(emit func(line []byte)) error {
	for _, t := range f.tails {
		if err := t.read(emit, f.onSkip); err != nil {
			return err
		}
	}
	return nil
}

func (f *Follower) close() {
	for _, t := range f.tails {
		t.closeFile()
	}
}

// tail tracks one file: its open handle, how far it has been read, and the
// identity it was opened with so rotation can be detected.
type tail struct {
	path   string
	file   *os.File
	offset int64
	info   os.FileInfo
	// startOffset is where the first open begins reading, and is cleared once
	// applied: later opens are rotations and always start at zero.
	startOffset int64
	splitter    lineSplitter
	buffer      []byte
}

func (t *tail) read(emit func(line []byte), drop func()) error {
	info, err := os.Stat(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			t.closeFile()
			return nil
		}
		return err
	}

	if t.file != nil && (!os.SameFile(info, t.info) || info.Size() < t.offset) {
		// Rotated or truncated underneath us: start over on the new file.
		t.closeFile()
	}
	if t.file == nil {
		file, err := os.Open(t.path) // #nosec G304 -- path is resolved from enclave's own state directory.
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		t.file = file
		t.info = info
		if t.startOffset > 0 {
			if t.startOffset <= info.Size() {
				if _, err := file.Seek(t.startOffset, io.SeekStart); err == nil {
					t.offset = t.startOffset
				}
			}
			t.startOffset = 0
		}
	}
	if info.Size() == t.offset {
		// Nothing appended since the last poll, and a held-back partial line
		// cannot be completed without new bytes.
		return nil
	}

	if t.buffer == nil {
		t.buffer = make([]byte, readChunkBytes)
	}
	for {
		read, err := t.file.Read(t.buffer)
		if read > 0 {
			t.offset += int64(read)
			t.splitter.feed(t.buffer[:read], emit, drop)
		}
		if err == io.EOF || read == 0 {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (t *tail) closeFile() {
	if t.file != nil {
		_ = t.file.Close()
		t.file = nil
	}
	t.splitter.reset()
	t.offset = 0
}
