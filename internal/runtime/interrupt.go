// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// interruptSignals cancel a start in progress. The foreground run also hands
// them to termtint, so the tint does not intercept a signal the run survives.
var interruptSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// interruptContext returns a context that SIGINT or SIGTERM cancels. A start
// still in progress observes the cancellation and tears down what it created.
// Registering the signals also keeps this process alive through them: once the
// session is attached, the engine child owns the terminal and receives Ctrl-C
// itself, and the post-session cleanup then runs to completion.
func interruptContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), interruptSignals...)
}
