// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package termtint marks a terminal that is currently owned by an enclave
// session by setting its background color, so a sandboxed session is visually
// distinct from an ordinary shell.
package termtint

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"sync"

	"golang.org/x/term"

	"enclave/internal/logx"
)

// OSC 11 sets the terminal background, OSC 111 resets it to the configured
// default. tmux 3.3+ consumes both to style the pane rather than forwarding
// them, so inside tmux only the session's pane is tinted.
const (
	setBackgroundFormat = "\x1b]11;%s\x07"
	resetBackground     = "\x1b]111\x07"
)

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Indirection for tests: the real emit path requires a terminal on stdout.
var (
	out        io.Writer = os.Stdout
	isTerminal           = func() bool {
		return term.IsTerminal(int(os.Stdout.Fd())) // #nosec G115 -- file descriptor from Fd() fits in int on all supported platforms.
	}
)

// Option adjusts how Begin reacts to interrupt signals.
type Option func(*options)

type options struct {
	callerHandled []os.Signal
}

// CallerHandles names interrupt signals the caller catches itself and survives,
// so that its deferred restore still runs. Begin leaves those signals alone:
// restoring early would drop the tint under a session that keeps running, and
// re-raising would kill the process before the caller's cleanup finishes. The
// remaining interrupt signals still restore the tint and keep their default
// effect.
func CallerHandles(signals ...os.Signal) Option {
	return func(o *options) {
		o.callerHandled = append(o.callerHandled, signals...)
	}
}

// Begin tints the terminal background and returns a function that restores it.
// Callers should defer the returned function; it is safe to call more than
// once. Begin is a no-op when color is empty or not an #rrggbb value, when the
// environment suppresses color, or when stdout is not a terminal. Config
// loading reports invalid values against the file that sets them; the format
// check here guards the escape sequence against anything that slips past.
//
// An interrupt signal not claimed through CallerHandles restores the tint and
// then keeps its default effect.
func Begin(color string, opts ...Option) func() {
	color = strings.TrimSpace(color)
	if !colorPattern.MatchString(color) {
		return func() {}
	}
	if logx.ColorSuppressedByEnv() || !isTerminal() {
		return func() {}
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	_, _ = fmt.Fprintf(out, setBackgroundFormat, color)

	var restoreOnce sync.Once
	restore := func() {
		restoreOnce.Do(func() {
			_, _ = fmt.Fprint(out, resetBackground)
		})
	}
	unwatch := watchInterrupts(interceptedSignals(o.callerHandled), restore)

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			unwatch()
			restore()
		})
	}
}

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

// watchInterrupts restores the tint and re-raises the signal when one of
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
	go func() {
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
	}
}
