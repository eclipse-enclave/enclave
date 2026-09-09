// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package prompt

import (
	"bufio"
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
	scanner := bufio.NewScanner(stdin)
	for attempt := 0; attempt < chooseAttempts; attempt++ {
		if _, err := fmt.Fprintf(stdout, "%s [%s]: ", msg, strings.Join(options, "/")); err != nil {
			return "", err
		}
		if !scanner.Scan() {
			return "", scanner.Err()
		}
		if choice, ok := matchChoice(strings.TrimSpace(scanner.Text()), options); ok {
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
