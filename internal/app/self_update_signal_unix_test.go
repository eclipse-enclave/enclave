// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/cli"
)

// interruptInstall sends the test process SIGINT from inside Install and waits
// for the signal to cancel Install's context, as a stalled download would. If
// the updater did not catch the signal, it kills the test binary.
func interruptInstall(ctx context.Context) error {
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return errors.New("SIGINT did not cancel the install")
	}
}

func TestSelfUpdateInterruptCancelsTheInstall(t *testing.T) {
	fake := &fakeSelfUpdater{installHook: interruptInstall}
	home := useFakeSelfUpdater(t, fake)
	var code int
	_, stderr := captureOutput(t, func() { code = runSelfUpdate(false) })
	if code != backend.ExitCodeInterrupted || !strings.Contains(stderr, "Enclave update interrupted.") {
		t.Fatalf("code = %d, stderr = %q; want an interrupted exit", code, stderr)
	}
	if stampExists(t, home) {
		t.Fatal("an interrupted update was recorded as a check")
	}
}

func TestAutomaticSelfUpdateInterruptStopsTheCommand(t *testing.T) {
	fake := &fakeSelfUpdater{installHook: interruptInstall}
	useFakeSelfUpdater(t, fake)
	var interrupted bool
	captureOutput(t, func() {
		interrupted = maybeAutomaticSelfUpdate(cli.Result{Action: "run"}, autoUpdateDefaults(true))
	})
	if !interrupted {
		t.Fatal("the command would run after the user interrupted the update")
	}
}
