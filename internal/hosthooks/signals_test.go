// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hosthooks

import (
	"os"
	"slices"
	"testing"
)

func TestInterceptedSignalsSkipCallerHandled(t *testing.T) {
	all := interruptSignals()
	if len(all) == 0 {
		t.Skip("no interrupt signals on this platform")
	}
	handled := all[:1]

	got := interceptedSignals(handled)
	if slices.ContainsFunc(got, func(sig os.Signal) bool { return slices.Contains(handled, sig) }) {
		t.Fatalf("caller-handled signal is still intercepted: %v", got)
	}
	if len(got) != len(all)-1 {
		t.Fatalf("expected %d intercepted signals, got %v", len(all)-1, got)
	}

	if got := interceptedSignals(all); len(got) != 0 {
		t.Fatalf("expected nothing intercepted when the caller handles every signal, got %v", got)
	}
}
