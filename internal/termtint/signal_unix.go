// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package termtint

import (
	"os"
	"os/signal"
	"syscall"
)

// interruptSignals are the signals that would terminate a session before a
// deferred restore can run.
func interruptSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}
}

// reraise restores the signal's default disposition and re-sends it, so the
// process dies exactly as it would have without the handler installed.
func reraise(sig os.Signal) {
	signal.Reset(sig)
	unixSignal, ok := sig.(syscall.Signal)
	if !ok {
		return
	}
	_ = syscall.Kill(syscall.Getpid(), unixSignal)
}
