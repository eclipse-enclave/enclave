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
)

var colorEnabled = resolveColorEnabled()

func Colorize(text string, color Color) string {
	if !colorEnabled || color == "" {
		return text
	}
	return string(color) + text + string(ColorReset)
}

func colorPrefix(label string, color Color) string {
	return Colorize(label+": ", color)
}

// ColorSuppressedByEnv reports whether the environment explicitly disables
// colored output, regardless of whether a terminal is attached. Consumers that
// paint something other than log text (see internal/termtint) honor the same
// opt-out without inheriting the stderr-based auto-detection below.
func ColorSuppressedByEnv() bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	return strings.ToLower(strings.TrimSpace(os.Getenv(model.EnvColor))) == "never"
}

func resolveColorEnabled() bool {
	if ColorSuppressedByEnv() {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(os.Getenv(model.EnvColor))) {
	case "always":
		return true
	case "", "auto":
		fallthrough
	default:
		return term.IsTerminal(int(os.Stderr.Fd())) // #nosec G115 -- file descriptor from Fd() fits in int on all supported platforms.
	}
}
