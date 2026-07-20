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

func logf(w io.Writer, label string, color Color, format string, args ...any) {
	prefix, ending := "", "\n"
	if isRawTTY(w) {
		prefix, ending = "\r", "\r\n"
	}
	_, _ = fmt.Fprintf(w, prefix+"%s"+format+ending, append([]any{colorPrefix(label, color)}, args...)...)
}

func Infof(format string, args ...any) {
	logf(os.Stdout, "info", ColorCyan, format, args...)
}

func Warnf(format string, args ...any) {
	logf(os.Stderr, "warn", ColorYellow, format, args...)
}

func Successf(format string, args ...any) {
	logf(os.Stdout, "ok", ColorGreen, format, args...)
}

func Errorf(format string, args ...any) {
	logf(os.Stderr, "error", ColorRed, format, args...)
}

func Debugf(format string, args ...any) {
	if level < Debug {
		return
	}
	logf(os.Stdout, "debug", ColorDim, format, args...)
}
