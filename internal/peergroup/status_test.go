// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package peergroup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestStatus(t *testing.T, state, reader string, gen Generation, applied, rejected Revision) {
	t.Helper()
	dir := StatusDir(state, "review")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := SupervisorStatus
	if reader == ReaderProxy {
		name = ProxyStatus
	}
	err := WriteStatus(filepath.Join(dir, name), StatusFile{
		Reader: reader, GroupGeneration: gen, Epoch: "9a8b7c6d5e4f3021",
		AppliedRevision: applied, RejectedRevision: rejected, Error: "hash mismatch",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWriteStatusAndRead(t *testing.T) {
	state := t.TempDir()
	w := NewStatusWaiter(state)
	if err := os.MkdirAll(StatusDir(state, "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	sup, proxy, err := w.Read("review")
	if err != nil || sup != nil || proxy != nil {
		t.Fatalf("Read with no files = %v %v %v", sup, proxy, err)
	}
	writeTestStatus(t, state, ReaderProxy, testGen, 4, 0)
	sup, proxy, err = w.Read("review")
	if err != nil || sup != nil || proxy == nil || proxy.AppliedRevision != 4 || proxy.SchemaVersion != SchemaVersion || proxy.UpdatedAt == "" {
		t.Fatalf("Read = %+v %+v %v", sup, proxy, err)
	}
	if err := WriteStatus(filepath.Join(StatusDir(state, "review"), "x.json"), StatusFile{Reader: "hub"}); err == nil {
		t.Fatal("WriteStatus accepted an unknown reader")
	}
	// A proxy writing into the supervisor's file is a bug the waiter must not trust.
	if err := WriteStatus(filepath.Join(StatusDir(state, "review"), SupervisorStatus), StatusFile{Reader: ReaderProxy}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Read("review"); err == nil {
		t.Fatal("Read accepted a status file naming the wrong reader")
	}
}

func TestWaitCommitted(t *testing.T) {
	ctx := context.Background()
	t.Run("both applied", func(t *testing.T) {
		state := t.TempDir()
		writeTestStatus(t, state, ReaderSupervisor, testGen, 7, 0)
		writeTestStatus(t, state, ReaderProxy, testGen, 6, 0)
		go func() {
			time.Sleep(120 * time.Millisecond)
			writeTestStatus(t, state, ReaderProxy, testGen, 7, 0)
		}()
		res, err := NewStatusWaiter(state).WaitCommitted(ctx, "review", testGen, 7, 5*time.Second)
		if err != nil || !res.Committed || res.Proxy.AppliedRevision != 7 {
			t.Fatalf("WaitCommitted = %+v, %v", res, err)
		}
	})
	t.Run("later revision counts", func(t *testing.T) {
		state := t.TempDir()
		writeTestStatus(t, state, ReaderSupervisor, testGen, 9, 0)
		writeTestStatus(t, state, ReaderProxy, testGen, 8, 0)
		if res, err := NewStatusWaiter(state).WaitCommitted(ctx, "review", testGen, 7, time.Second); err != nil || !res.Committed {
			t.Fatalf("WaitCommitted = %+v, %v", res, err)
		}
	})
	t.Run("rejected", func(t *testing.T) {
		state := t.TempDir()
		writeTestStatus(t, state, ReaderSupervisor, testGen, 6, 7)
		writeTestStatus(t, state, ReaderProxy, testGen, 7, 0)
		start := time.Now()
		res, err := NewStatusWaiter(state).WaitCommitted(ctx, "review", testGen, 7, 5*time.Second)
		if !errors.Is(err, ErrRevisionRefused) || res.Committed || !strings.Contains(err.Error(), "hash mismatch") {
			t.Fatalf("WaitCommitted = %+v, %v", res, err)
		}
		if time.Since(start) > time.Second {
			t.Fatal("rejection did not fail immediately")
		}
	})
	t.Run("old generation never commits", func(t *testing.T) {
		state := t.TempDir()
		writeTestStatus(t, state, ReaderSupervisor, "ffffffffffffffff", 9, 0)
		writeTestStatus(t, state, ReaderProxy, "ffffffffffffffff", 9, 7)
		_, err := NewStatusWaiter(state).WaitCommitted(ctx, "review", testGen, 7, 150*time.Millisecond)
		if !errors.Is(err, ErrNotCommitted) {
			t.Fatalf("WaitCommitted = %v, want ErrNotCommitted", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		state := t.TempDir()
		writeTestStatus(t, state, ReaderSupervisor, testGen, 7, 0)
		res, err := NewStatusWaiter(state).WaitCommitted(ctx, "review", testGen, 7, 150*time.Millisecond)
		if !errors.Is(err, ErrNotCommitted) || res.Committed || res.Supervisor == nil || res.Proxy != nil {
			t.Fatalf("WaitCommitted = %+v, %v", res, err)
		}
		if !strings.Contains(err.Error(), "proxy has no status") {
			t.Fatalf("timeout error lacks detail: %v", err)
		}
	})
	t.Run("context canceled", func(t *testing.T) {
		state := t.TempDir()
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := NewStatusWaiter(state).WaitCommitted(cctx, "review", testGen, 7, time.Minute)
		if !errors.Is(err, ErrNotCommitted) || !errors.Is(err, context.Canceled) {
			t.Fatalf("WaitCommitted = %v", err)
		}
	})
}
