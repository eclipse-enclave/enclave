// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package netlog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// collector gathers events from a follower running in the background.
type collector struct {
	mu     sync.Mutex
	events []Event
}

func (c *collector) emit(event Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *collector) domains() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.events))
	for _, event := range c.events {
		names = append(names, event.Domain)
	}
	return names
}

func (c *collector) waitFor(t *testing.T, count int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if names := c.domains(); len(names) >= count {
			return names
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d events, got %v", count, c.domains())
	return nil
}

func appendLine(t *testing.T, path string, domain string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()
	line := fmt.Sprintf(`{"ts":%q,"type":"tcp","verdict":"pass","domain":%q}`+"\n", time.Now().UTC().Format(TimeFormat), domain)
	if _, err := file.WriteString(line); err != nil {
		t.Fatalf("append to %s: %v", path, err)
	}
}

func startFollower(t *testing.T, paths []string, opts FollowOptions) *collector {
	t.Helper()
	if opts.Interval == 0 {
		opts.Interval = 5 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	sink := &collector{}
	done := make(chan error, 1)
	// Constructed here, not in the goroutine: the follower records the current
	// log size when it is built, so a test that appends immediately must not
	// race with construction.
	follower := NewFollower(paths, opts)
	go func() {
		done <- follower.Run(ctx, sink.emit)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Follower.Run() error = %v", err)
		}
	})
	return sink
}

func TestFollowerEmitsAppendedEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	appendLine(t, path, "before.example")

	sink := startFollower(t, []string{path}, FollowOptions{FromStart: true})
	appendLine(t, path, "after.example")

	got := sink.waitFor(t, 2)
	if got[0] != "before.example" || got[1] != "after.example" {
		t.Fatalf("events = %v", got)
	}
}

func TestFollowerSkipsHistoryByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	appendLine(t, path, "before.example")

	sink := startFollower(t, []string{path}, FollowOptions{})
	appendLine(t, path, "after.example")

	got := sink.waitFor(t, 1)
	if got[0] != "after.example" {
		t.Fatalf("events = %v, want only what arrived after the follow started", got)
	}
}

func TestFollowerStartsAtTheGivenOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	appendLine(t, path, "backlog.example")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	// A caller that printed the backlog itself continues from where its read
	// stopped, so an event appended in between is neither lost nor duplicated.
	appendLine(t, path, "between.example")

	sink := startFollower(t, []string{path}, FollowOptions{StartOffsets: map[string]int64{path: info.Size()}})
	appendLine(t, path, "after.example")

	got := sink.waitFor(t, 2)
	if got[0] != "between.example" || got[1] != "after.example" {
		t.Fatalf("events = %v, want everything after the given offset", got)
	}
}

func TestFollowerDropsAnUnterminatedOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	sink := startFollower(t, []string{path}, FollowOptions{})

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	// A torn write with no newline in sight must not be buffered indefinitely.
	if _, err := file.WriteString(strings.Repeat("x", maxLineBytes+1024) + "\n"); err != nil {
		t.Fatalf("write torn line: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
	appendLine(t, path, "after.example")

	got := sink.waitFor(t, 1)
	if len(got) != 1 || got[0] != "after.example" {
		t.Fatalf("events = %v, want only the line after the dropped one", got)
	}
}

func TestFollowerReopensAfterRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.log")
	appendLine(t, path, "first.example")

	sink := startFollower(t, []string{path}, FollowOptions{FromStart: true})
	sink.waitFor(t, 1)

	if err := os.Rename(path, RotatedPath(path)); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	appendLine(t, path, "second.example")

	got := sink.waitFor(t, 2)
	if got[1] != "second.example" {
		t.Fatalf("events = %v, want the follower to reopen the new file", got)
	}
}

func TestFollowerReopensAfterTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	appendLine(t, path, "first.example")

	sink := startFollower(t, []string{path}, FollowOptions{FromStart: true})
	sink.waitFor(t, 1)

	if err := os.Truncate(path, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// Give the follower a poll to notice the file shrank. Size is the only
	// signal for an in-place truncation, so a writer that truncates and refills
	// between two polls is indistinguishable from an append.
	time.Sleep(50 * time.Millisecond)
	appendLine(t, path, "second.example")

	got := sink.waitFor(t, 2)
	if got[1] != "second.example" {
		t.Fatalf("events = %v, want the follower to restart at the new offset", got)
	}
}

func TestFollowerPicksUpAFileThatDoesNotExistYet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")

	sink := startFollower(t, []string{path}, FollowOptions{})
	appendLine(t, path, "late.example")

	if got := sink.waitFor(t, 1); got[0] != "late.example" {
		t.Fatalf("events = %v", got)
	}
}

func TestFollowerHoldsBackPartialLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	sink := startFollower(t, []string{path}, FollowOptions{})

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := file.WriteString(`{"ts":"2026-08-13T12:00:00Z","type":"tcp",`); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if names := sink.domains(); len(names) != 0 {
		t.Fatalf("emitted %v from a partial line", names)
	}
	if _, err := file.WriteString(`"verdict":"pass","domain":"split.example"}` + "\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := sink.waitFor(t, 1); got[0] != "split.example" {
		t.Fatalf("events = %v", got)
	}
}

func TestFollowerCountsUnparseableLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.log")
	var mu sync.Mutex
	skipped := 0
	sink := startFollower(t, []string{path}, FollowOptions{OnSkipped: func() {
		mu.Lock()
		defer mu.Unlock()
		skipped++
	}})

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := file.WriteString("Aug 13 12:04:35 dnsmasq[12]: config x.example is NXDOMAIN\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	appendLine(t, path, "valid.example")
	sink.waitFor(t, 1)

	mu.Lock()
	defer mu.Unlock()
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
}
