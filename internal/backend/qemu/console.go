// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package qemu

import (
	"os"

	"golang.org/x/term"

	"enclave/internal/backend"
)

// consoleSize is the window size the guest console is initialized to.
type consoleSize struct {
	Rows int
	Cols int
}

// defaultConsoleSize is the conventional terminal size, used when the host
// stdio carries no window size (output piped to a file, CI).
var defaultConsoleSize = consoleSize{Rows: 24, Cols: 80}

// resolveConsoleSize reads the window size of the host terminal driving the
// session. The emulated serial console has no window size of its own, so
// unless the guest is told, its console stays 0x0: tools that trust the
// terminal render one column wide, and those that do not fall back to 80x24
// regardless of the real terminal.
func resolveConsoleSize(attach backend.AttachIO) consoleSize {
	var candidates []*os.File
	appendFile := func(value any) {
		if file, ok := value.(*os.File); ok && file != nil {
			candidates = append(candidates, file)
		}
	}
	// The session's own stdio first, then the process stdio the backend falls
	// back to when the caller leaves AttachIO empty.
	appendFile(attach.Out)
	appendFile(attach.Err)
	appendFile(attach.In)
	candidates = append(candidates, os.Stdout, os.Stderr, os.Stdin)
	for _, file := range candidates {
		cols, rows, err := term.GetSize(int(file.Fd())) // #nosec G115 -- file descriptor from Fd() fits in int on all supported platforms.
		if err != nil || cols <= 0 || rows <= 0 {
			continue
		}
		return consoleSize{Rows: rows, Cols: cols}
	}
	return defaultConsoleSize
}
