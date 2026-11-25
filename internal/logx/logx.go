// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package logx

import (
	"fmt"
	"io"
	"os"
	"strings"

	"enclave/internal/model"
)

type Level int

const (
	Info Level = iota
	Debug
)

var level = Info

func init() {
	SetLevel(os.Getenv(model.EnvLogLevel))
}

func SetLevel(value string) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug", "verbose", "trace":
		level = Debug
	case "info", "":
		level = Info
	}
}

// isRawTTY is a variable so tests can stub the terminal state.
var isRawTTY = writerIsRawTTY

// stdoutReserved sends every message to stderr; see ReserveStdout.
var stdoutReserved bool

// ReserveStdout sends every message to stderr from now on, for a command whose
// stdout is data only. It returns a function that restores the default.
func ReserveStdout() (restore func()) {
	previous := stdoutReserved
	stdoutReserved = true
	return func() { stdoutReserved = previous }
}

// stdout is where Infof, Successf, and Debugf write.
func stdout() io.Writer {
	if stdoutReserved {
		return os.Stderr
	}
	return os.Stdout
}

func logf(w io.Writer, label string, color Color, format string, args ...any) {
	prefix, ending := "", "\n"
	if isRawTTY(w) {
		prefix, ending = "\r", "\r\n"
	}
	_, _ = fmt.Fprintf(w, prefix+"%s"+format+ending, append([]any{colorPrefix(label, color)}, args...)...)
}

func Infof(format string, args ...any) {
	logf(stdout(), "info", ColorCyan, format, args...)
}

// Noticef is Infof on stderr, for messages incidental to a command whose
// stdout may be redirected or parsed.
func Noticef(format string, args ...any) {
	logf(os.Stderr, "info", ColorCyan, format, args...)
}

func Warnf(format string, args ...any) {
	logf(os.Stderr, "warn", ColorYellow, format, args...)
}

func Successf(format string, args ...any) {
	logf(stdout(), "ok", ColorGreen, format, args...)
}

// SuccessNoticef is Successf on stderr, the success counterpart of Noticef.
func SuccessNoticef(format string, args ...any) {
	logf(os.Stderr, "ok", ColorGreen, format, args...)
}

func Errorf(format string, args ...any) {
	logf(os.Stderr, "error", ColorRed, format, args...)
}

func Debugf(format string, args ...any) {
	if level < Debug {
		return
	}
	logf(stdout(), "debug", ColorDim, format, args...)
}

// DebugNoticef is Debugf on stderr, the debug-level counterpart of Noticef.
func DebugNoticef(format string, args ...any) {
	if level < Debug {
		return
	}
	logf(os.Stderr, "debug", ColorDim, format, args...)
}
