// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package a2aagent

import (
	"context"
	"io"
	"os/exec"
)

// Invocation is one headless harness turn.
type Invocation struct {
	// Prompt includes the provenance line; drivers pass it on stdin, never argv.
	Prompt string
	// HarnessSessionID is empty for a new context, else the session to resume.
	HarnessSessionID string
	// NewHarnessSessionID is the Driver.NewSessionID result for a new context, already bound in the table.
	NewHarnessSessionID string
	WorkDir             string
	// Env is the harness environment; the executor removes every ENCLAVE_A2A_* variable.
	Env []string
}

// EventKind classifies a decoded harness event.
type EventKind string

const (
	EventSession  EventKind = "session"
	EventProgress EventKind = "progress"
	EventFinal    EventKind = "final"
)

// HarnessEvent is one decoded harness event.
type HarnessEvent struct {
	Kind             EventKind
	HarnessSessionID string
	Text             string
}

// Outcome is what a finished harness run produced.
type Outcome struct {
	HarnessSessionID string
	FinalMessage     string
	// Transcripts are config-store-relative transcript paths for retention.
	Transcripts []string
}

// Driver maps one harness (from the tool's spec.yaml a2a block) to commands
// and events. It never starts or kills processes; the Executor does.
type Driver interface {
	Name() string
	// CheckVersion fails when the installed harness is outside the pinned range.
	CheckVersion(ctx context.Context) error
	// NewSessionID picks the session ID of a new context before spawn (CR-A9-1); empty when the harness picks it.
	NewSessionID() (string, error)
	// Command builds the process; the executor sets the process group and stdin.
	Command(ctx context.Context, inv Invocation) (*exec.Cmd, error)
	// Decode reads until EOF; unknown events are logged and skipped, EOF without an outcome event is an error (CR-P2-2).
	Decode(r io.Reader, emit func(HarnessEvent)) (Outcome, error)
}

// InvocationInventory is optional for drivers with storage paths known after
// Command. The executor persists these config-store-relative paths before
// starting the harness, including when the harness chooses its own session ID.
// Decode's Outcome can add paths learned from the stream later.
type InvocationInventory interface {
	InvocationTranscripts(Invocation) []string
}
