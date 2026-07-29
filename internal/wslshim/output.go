// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

import (
	"strings"
	"unicode/utf16"
)

// decodeWSLOutput turns bytes written by wsl.exe into a Go string.
//
// wsl.exe emits its own output — the distribution list, its error messages — as
// UTF-16LE, while bytes it relays from a Linux process pass through unchanged.
// The launcher reads both, so it sniffs the encoding rather than assuming one.
func decodeWSLOutput(raw []byte) string {
	if isUTF16LE(raw) {
		units := make([]uint16, 0, len(raw)/2)
		for i := 0; i+1 < len(raw); i += 2 {
			units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
		}
		return strings.TrimPrefix(string(utf16.Decode(units)), "\ufeff")
	}
	return strings.TrimPrefix(string(raw), "\ufeff")
}

// isUTF16LE detects the encoding from a byte-order mark, or failing that from
// the NUL padding that ASCII text acquires when stored as UTF-16LE.
func isUTF16LE(raw []byte) bool {
	if len(raw) < 2 {
		return false
	}
	if raw[0] == 0xFF && raw[1] == 0xFE {
		return true
	}
	// Valid UTF-8 never contains a NUL byte in practice, so any high-byte NUL
	// in an even position is decisive. Requiring a majority avoids misreading a
	// stray NUL in otherwise-UTF-8 output.
	padded, total := 0, 0
	for i := 0; i+1 < len(raw); i += 2 {
		total++
		if raw[i+1] == 0x00 && raw[i] != 0x00 {
			padded++
		}
	}
	return total > 0 && padded*2 > total
}

// parseDistroList reads the output of `wsl.exe --list --quiet`, one
// distribution name per line.
func parseDistroList(raw []byte) []string {
	var names []string
	for _, line := range strings.Split(decodeWSLOutput(raw), "\n") {
		// Trim CR from the Windows line endings and the NULs that a
		// misdetected encoding can leave behind.
		name := strings.TrimSpace(strings.Trim(line, "\r\x00"))
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}
