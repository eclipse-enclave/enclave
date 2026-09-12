// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package termtint

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// A caller that runs its start under signal.NotifyContext survives SIGINT and
// restores through its deferred call. Begin must neither restore early nor
// re-raise the signal, which would kill the process while the caller is still
// cleaning up.
func TestCallerHandledSignalLeavesTintAndProcessAlone(t *testing.T) {
	buf := withTerminal(t, true)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	t.Cleanup(stop)

	restore := Begin("#2a0f12", CallerHandles(os.Interrupt, syscall.SIGTERM))
	t.Cleanup(restore)
	buf.Reset()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("SIGINT did not reach the caller's context")
	}
	// A watcher wrongly registered for SIGINT would act within this window.
	time.Sleep(50 * time.Millisecond)
	if got := buf.String(); got != "" {
		t.Fatalf("tint was touched on a caller-handled signal: %q", got)
	}

	restore()
	if got := buf.String(); got != "\x1b]111\x07" {
		t.Fatalf("unexpected restore sequence: %q", got)
	}
}
