// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package hosthooks

import (
	"os"
	"os/signal"
	"slices"
)

// interceptedSignals are the interrupt signals Begin handles itself: the
// platform's interrupt set minus those the caller handles.
func interceptedSignals(callerHandled []os.Signal) []os.Signal {
	var intercepted []os.Signal
	for _, sig := range interruptSignals() {
		if !slices.Contains(callerHandled, sig) {
			intercepted = append(intercepted, sig)
		}
	}
	return intercepted
}

// watchInterrupts restores terminal effects and re-raises the signal when one of
// signals arrives. A signal that kills enclave outright would leave the
// terminal tinted with no session behind it; a stale marker is worse than none,
// so restore before dying and then let the signal take its default effect. The
// returned function stops watching. Nothing is registered for an empty list,
// since signal.Notify without signals would subscribe to all of them.
func watchInterrupts(signals []os.Signal, restore func()) func() {
	if len(signals) == 0 {
		return func() {}
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, signals...)
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case sig := <-ch:
			signal.Stop(ch)
			restore()
			reraise(sig)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
		<-finished
	}
}
