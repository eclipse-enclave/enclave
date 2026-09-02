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

// Begin tints the terminal background and returns a function that restores it.
// Callers should defer the returned function; it is safe to call more than
// once. Begin is a no-op when color is empty or not an #rrggbb value, when the
// environment suppresses color, or when stdout is not a terminal. Config
// loading reports invalid values against the file that sets them; the format
// check here guards the escape sequence against anything that slips past.
func Begin(color string) func() {
	color = strings.TrimSpace(color)
	if !colorPattern.MatchString(color) {
		return func() {}
	}
	if logx.ColorSuppressedByEnv() || !isTerminal() {
		return func() {}
	}

	_, _ = fmt.Fprintf(out, setBackgroundFormat, color)

	var restoreOnce sync.Once
	// A signal that kills enclave outright would leave the terminal tinted with
	// no session behind it. A stale marker is worse than none, so restore before
	// dying and then let the signal take its default effect.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, interruptSignals()...)
	restore := func() {
		restoreOnce.Do(func() {
			signal.Stop(signals)
			_, _ = fmt.Fprint(out, resetBackground)
		})
	}

	done := make(chan struct{})
	go func() {
		select {
		case sig := <-signals:
			restore()
			reraise(sig)
		case <-done:
		}
	}()

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			close(done)
			restore()
		})
	}
}
