// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

import (
	"fmt"
	"strings"
)

// maxCommandLine is the CreateProcess limit on the command-line string,
// including its terminating NUL, measured in UTF-16 code units.
const maxCommandLine = 32767

// buildCommandLine renders the full command line, argv[0] included.
//
// Windows passes a single string to CreateProcess and the callee re-parses it.
// Enclave's arguments are free-form — prompts with spaces and quotes, and
// everything after -- goes to the agent verbatim — so the launcher builds the
// string itself instead of letting os/exec do it, and hands it to CreateProcess
// unchanged. The escaping is the standard CommandLineToArgvW inverse, which is
// what wsl.exe's own parsing expects.
func buildCommandLine(exe string, args []string) (string, error) {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, escapeArg(exe))
	for _, arg := range args {
		parts = append(parts, escapeArg(arg))
	}
	cmdLine := strings.Join(parts, " ")

	// The limit counts the terminating NUL, so the string itself must stay
	// strictly below it.
	if length := utf16Len(cmdLine); length >= maxCommandLine {
		return "", fmt.Errorf("the arguments are too long for Windows to pass on: the command line would "+
			"be %d UTF-16 characters and CreateProcess accepts at most %d. Shorten the arguments, or pass "+
			"long input through a file", length, maxCommandLine-1)
	}
	return cmdLine, nil
}

// escapeArg quotes one argument so CommandLineToArgvW reproduces it exactly.
// Backslashes are only special immediately before a quote or at the end of a
// quoted argument, which is why they are counted rather than escaped eagerly.
func escapeArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if !strings.ContainsAny(arg, " \t\n\v\"") {
		return arg
	}

	var out strings.Builder
	out.WriteByte('"')
	backslashes := 0
	// Iterating bytes is safe: every special character is ASCII, and UTF-8
	// continuation bytes are all >= 0x80, so multi-byte runes pass through.
	for i := 0; i < len(arg); i++ {
		switch c := arg[i]; c {
		case '\\':
			backslashes++
		case '"':
			// Escape the run of backslashes, then the quote itself.
			out.WriteString(strings.Repeat(`\`, backslashes*2+1))
			out.WriteByte('"')
			backslashes = 0
		default:
			out.WriteString(strings.Repeat(`\`, backslashes))
			out.WriteByte(c)
			backslashes = 0
		}
	}
	// A trailing run would otherwise escape the closing quote.
	out.WriteString(strings.Repeat(`\`, backslashes*2))
	out.WriteByte('"')
	return out.String()
}

// utf16Len counts UTF-16 code units, so a character outside the BMP costs the
// two units Windows actually stores.
func utf16Len(s string) int {
	length := 0
	for _, r := range s {
		if r > 0xFFFF {
			length += 2
			continue
		}
		length++
	}
	return length
}
