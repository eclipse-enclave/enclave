// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package extinstall

import (
	"fmt"
	"io"
	"strings"
	"time"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/prompt"
)

// Env carries the ambient dependencies of an install run.
type Env struct {
	Paths   model.Paths
	Home    string
	Fetcher Fetcher
	Stdin   io.Reader
	// Narration is where this package's human-facing lines go: discovery,
	// warnings, capability summaries, per-extension outcomes, hints, and the
	// text of any confirmation prompt. A nil Narration renders none of it,
	// which is what a caller whose whole report is the result envelope wants.
	// It is separate from that envelope, which the caller writes itself.
	Narration io.Writer
	// Style decorates that narration. The zero value is plain text at a
	// default width, which is what a pipe and a test buffer want.
	Style   Style
	Now     func() time.Time
	Version string
}

func (e Env) now() time.Time {
	if e.Now == nil {
		return time.Now().UTC()
	}
	return e.Now()
}

// narrate is the narration stream, or a sink when there is none.
func (e Env) narrate() io.Writer {
	if e.Narration == nil {
		return io.Discard
	}
	return e.Narration
}

// confirm asks the user a yes/no question on the narration stream. The answer
// is followed by a blank line so what happens next starts on a line of its
// own: a piped stdin echoes nothing, and without this the outcome would be
// appended to the prompt.
func (e Env) confirm(question string) (bool, error) {
	if e.Narration == nil {
		return false, fmt.Errorf("cannot ask for confirmation without an output stream; pass --yes")
	}
	answer, err := prompt.Confirm(question, e.Stdin, e.Narration)
	_, _ = fmt.Fprintln(e.Narration)
	return answer, err
}

// kindDir is the user extension directory this run installs into.
func (e Env) kindDir(kind model.ExtensionKind) string {
	_, userRoot := config.ExtensionRoots(e.Paths, kind)
	return userRoot
}

// lockPath is the host lock that serializes staging and swapping.
func (e Env) lockPath() string {
	if strings.TrimSpace(e.Home) == "" {
		return ""
	}
	return config.HostLockPath(e.Home, "extensions.lock")
}
