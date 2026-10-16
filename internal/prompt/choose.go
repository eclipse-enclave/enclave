// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package prompt

import (
	"fmt"
	"io"
	"strings"
)

// chooseAttempts bounds how often Choose re-asks after an unrecognized answer.
const chooseAttempts = 3

// Choose prints msg followed by the options and reads one line from stdin,
// re-asking a few times after an unrecognized answer. An answer matches an
// option case-insensitively, either in full or by a first letter that is
// unique among the options. EOF or exhausted attempts return "" without an
// error so callers can fall back to a default.
func Choose(msg string, options []string, stdin io.Reader, stdout io.Writer) (string, error) {

	for attempt := 0; attempt < chooseAttempts; attempt++ {
		if _, err := fmt.Fprintf(stdout, "%s [%s]: ", msg, strings.Join(options, "/")); err != nil {
			return "", err
		}
		line, err := readChoiceLine(stdin)
		if err != nil && err != io.EOF {
			return "", err
		}
		if line == "" && err == io.EOF {
			return "", nil
		}
		if choice, ok := matchChoice(strings.TrimSpace(line), options); ok {
			return choice, nil
		}
	}
	return "", nil
}

func matchChoice(answer string, options []string) (string, bool) {
	answer = strings.ToLower(answer)
	if answer == "" {
		return "", false
	}
	var prefixMatch string
	prefixMatches := 0
	for _, option := range options {
		lower := strings.ToLower(option)
		if answer == lower {
			return option, true
		}
		if len(answer) == 1 && strings.HasPrefix(lower, answer) {
			prefixMatch = option
			prefixMatches++
		}
	}
	if prefixMatches == 1 {
		return prefixMatch, true
	}
	return "", false
}

// readChoiceLine avoids buffering bytes intended for the eventual session process.
func readChoiceLine(in io.Reader) (string, error) {
	var line strings.Builder
	var one [1]byte
	for line.Len() < 4096 {
		n, err := in.Read(one[:])
		if n > 0 {
			if one[0] == '\n' {
				return line.String(), nil
			}
			line.WriteByte(one[0])
		}
		if err != nil {
			return line.String(), err
		}
		if n == 0 {
			return line.String(), io.ErrNoProgress
		}
	}
	return line.String(), fmt.Errorf("prompt answer exceeds 4096 bytes")
}
