// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package termtint

import (
	"bytes"
	"strings"
	"testing"
)

// withTerminal points the package at a buffer and pretends stdout is a
// terminal, restoring the real values afterwards.
func withTerminal(t *testing.T, terminal bool) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prevOut, prevIsTerminal := out, isTerminal
	out, isTerminal = buf, func() bool { return terminal }
	t.Cleanup(func() { out, isTerminal = prevOut, prevIsTerminal })
	return buf
}

func TestBeginTintsAndRestores(t *testing.T) {
	buf := withTerminal(t, true)

	restore := Begin("#2a0f12")
	if got := buf.String(); got != "\x1b]11;#2a0f12\x07" {
		t.Fatalf("unexpected tint sequence: %q", got)
	}

	buf.Reset()
	restore()
	if got := buf.String(); got != "\x1b]111\x07" {
		t.Fatalf("unexpected restore sequence: %q", got)
	}

	buf.Reset()
	restore()
	if got := buf.String(); got != "" {
		t.Fatalf("restore is not idempotent, second call wrote %q", got)
	}
}

func TestBeginSkips(t *testing.T) {
	cases := []struct {
		name     string
		color    string
		terminal bool
		env      map[string]string
	}{
		{name: "empty color", color: "", terminal: true},
		{name: "blank color", color: "   ", terminal: true},
		{name: "missing hash", color: "2a0f12", terminal: true},
		{name: "short color", color: "#2a0", terminal: true},
		{name: "named color", color: "red", terminal: true},
		{name: "not a terminal", color: "#2a0f12", terminal: false},
		{name: "NO_COLOR", color: "#2a0f12", terminal: true, env: map[string]string{"NO_COLOR": "1"}},
		{name: "ENCLAVE_COLOR never", color: "#2a0f12", terminal: true, env: map[string]string{"ENCLAVE_COLOR": "never"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			buf := withTerminal(t, tc.terminal)

			restore := Begin(tc.color)
			restore()

			if got := buf.String(); got != "" {
				t.Fatalf("expected no output, got %q", got)
			}
		})
	}
}

// ENCLAVE_COLOR=always may only veto a tint, never force one on: the config key
// stays the single on/off switch.
func TestColorAlwaysDoesNotForceTint(t *testing.T) {
	t.Setenv("ENCLAVE_COLOR", "always")
	buf := withTerminal(t, true)

	restore := Begin("")
	restore()

	if got := buf.String(); got != "" {
		t.Fatalf("expected no output without a configured color, got %q", got)
	}
}

func TestBeginAcceptsUppercaseHex(t *testing.T) {
	buf := withTerminal(t, true)

	restore := Begin("#2A0F12")
	t.Cleanup(restore)

	if !strings.Contains(buf.String(), "#2A0F12") {
		t.Fatalf("expected uppercase color to be emitted, got %q", buf.String())
	}
}
