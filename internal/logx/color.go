// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package logx

import (
	"os"
	"strings"

	"golang.org/x/term"

	"enclave/internal/model"
)

type Color string

const (
	ColorReset  Color = "\x1b[0m"
	ColorRed    Color = "\x1b[31m"
	ColorYellow Color = "\x1b[33m"
	ColorGreen  Color = "\x1b[32m"
	ColorCyan   Color = "\x1b[36m"
	ColorDim    Color = "\x1b[2m"
	ColorBold   Color = "\x1b[1m"
)

var colorEnabled = ColorEnabledFor(os.Stderr)

// ColorEnabledFor reports whether ANSI styling should be written to f, for
// callers that render to a stream other than the log stream. NO_COLOR wins
// outright, then ENCLAVE_COLOR, and otherwise f has to be a terminal.
func ColorEnabledFor(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(model.EnvColor))) {
	case "always":
		return true
	case "never":
		return false
	}
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd())) // #nosec G115 -- file descriptor from Fd() fits in int on all supported platforms.
}

func Colorize(text string, color Color) string {
	if !colorEnabled || color == "" {
		return text
	}
	return string(color) + text + string(ColorReset)
}

func colorPrefix(label string, color Color) string {
	return Colorize(label+": ", color)
}
