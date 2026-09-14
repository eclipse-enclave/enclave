// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildLogForwardsAndKeepsTail(t *testing.T) {
	var forwarded bytes.Buffer
	log := NewBuildLog(&forwarded)
	if _, err := log.Write([]byte("step 1\nstep 2\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if forwarded.String() != "step 1\nstep 2\n" {
		t.Fatalf("forwarded output = %q", forwarded.String())
	}
	if log.Tail() != "step 1\nstep 2\n" {
		t.Fatalf("tail = %q", log.Tail())
	}
}

func TestBuildLogTailIsBounded(t *testing.T) {
	log := NewBuildLog(nil)
	log.max = 16
	for i := 0; i < 10; i++ {
		if _, err := log.Write([]byte("0123456789")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	tail := log.Tail()
	if len(tail) != 16 || !strings.HasSuffix(tail, "0123456789") {
		t.Fatalf("expected the last 16 bytes, got %q", tail)
	}
	// A single write larger than the bound keeps its last bytes.
	if _, err := log.Write([]byte(strings.Repeat("a", 30) + "END")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if tail := log.Tail(); len(tail) != 16 || !strings.HasSuffix(tail, "END") {
		t.Fatalf("expected the last 16 bytes of the large write, got %q", tail)
	}
}

func TestBuildLogTracksLastPlainLine(t *testing.T) {
	log := NewBuildLog(nil)
	for _, chunk := range []string{"\x1b[36mSTEP 3/9:\x1b[0m RUN apt-get ", "update\r\n", "  \n", "go: downloading golang.org/x/vuln"} {
		if _, err := log.Write([]byte(chunk)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if got := log.LastLine(); got != "STEP 3/9: RUN apt-get update" {
		t.Fatalf("last line = %q; partial and blank lines must not count, escapes must be stripped", got)
	}
	if _, err := log.Write([]byte(" v1.1.4\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := log.LastLine(); got != "go: downloading golang.org/x/vuln v1.1.4" {
		t.Fatalf("last line = %q; a line split across writes must be joined", got)
	}
}

func TestBuildLogWarnsWhenStalled(t *testing.T) {
	log := NewBuildLog(nil)
	var mu sync.Mutex
	clock := time.Unix(0, 0)
	log.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	log.lastAt = clock
	log.poll = 5 * time.Millisecond
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }

	type warning struct {
		idle time.Duration
		line string
	}
	warnings := make(chan warning, 8)
	stop := log.WarnWhenStalled(time.Minute, func(idle time.Duration, line string) {
		warnings <- warning{idle, line}
	})
	defer stop()

	if _, err := log.Write([]byte("Installing feature (user): devtools\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	advance(30 * time.Second)
	select {
	case w := <-warnings:
		t.Fatalf("warned too early: %+v", w)
	case <-time.After(50 * time.Millisecond):
	}

	advance(31 * time.Second)
	select {
	case w := <-warnings:
		if w.idle < time.Minute || w.line != "Installing feature (user): devtools" {
			t.Fatalf("unexpected warning %+v", w)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a stall warning after a minute of silence")
	}

	// Still silent: no second warning until another full period has passed.
	advance(30 * time.Second)
	select {
	case w := <-warnings:
		t.Fatalf("warned again before a full period elapsed: %+v", w)
	case <-time.After(50 * time.Millisecond):
	}
	advance(31 * time.Second)
	select {
	case <-warnings:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a repeated stall warning after another period")
	}

	// Output resumes: the idle clock resets and no warning follows.
	if _, err := log.Write([]byte("go: downloading\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	advance(59 * time.Second)
	select {
	case w := <-warnings:
		t.Fatalf("warned after output resumed: %+v", w)
	case <-time.After(50 * time.Millisecond):
	}
}
