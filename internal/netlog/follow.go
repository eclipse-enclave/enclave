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
	// OnSkipped reports lines that could not be parsed, once per line.
	OnSkipped func()
}

// Follower streams events appended to one or more log files. Files that do not
// exist yet are picked up when they appear, and a rotation (truncation or a new
// inode at the same path) reopens the file from the start.
type Follower struct {
	paths    []string
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
		paths:    append([]string(nil), paths...),
		interval: interval,
		onSkip:   opts.OnSkipped,
	}
	for _, path := range follower.paths {
		// Without FromStart, history is skipped by remembering how large the
		// log was when the follow began. Recording it here rather than at the
		// first read keeps events appended in between from being lost, and lets
		// a log that only appears later be read from its start.
		var startOffset int64
		if !opts.FromStart {
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
// the caller. The gateway's DNS translator follows a dnsmasq log this way.
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
		if err := t.read(emit); err != nil {
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
	// startOffset is where the first open begins reading. Later opens are
	// rotations and always start at zero.
	startOffset  int64
	startApplied bool
	partial      []byte
}

func (t *tail) read(emit func(line []byte)) error {
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
		t.partial = nil
		t.offset = 0
		if !t.startApplied {
			t.startApplied = true
			if t.startOffset > 0 && t.startOffset <= info.Size() {
				if _, err := file.Seek(t.startOffset, io.SeekStart); err == nil {
					t.offset = t.startOffset
				}
			}
		}
	}

	for {
		buffer := make([]byte, 32*1024)
		read, err := t.file.Read(buffer)
		if read > 0 {
			t.offset += int64(read)
			t.consume(buffer[:read], emit)
		}
		if err == io.EOF || read == 0 {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// consume splits the chunk into lines, holding back a trailing partial line
// until the writer finishes it.
func (t *tail) consume(chunk []byte, emit func(line []byte)) {
	t.partial = append(t.partial, chunk...)
	for {
		index := bytes.IndexByte(t.partial, '\n')
		if index < 0 {
			return
		}
		line := make([]byte, index)
		copy(line, t.partial[:index])
		t.partial = t.partial[index+1:]
		emit(line)
	}
}

func (t *tail) closeFile() {
	if t.file != nil {
		_ = t.file.Close()
		t.file = nil
	}
	t.partial = nil
	t.offset = 0
}
