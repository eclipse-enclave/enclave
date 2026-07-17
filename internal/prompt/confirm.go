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

// Confirm prints msg followed by " [y/N]: " to stdout and reads one line from
// stdin. It returns true only when the user enters "y" or "yes"
// (case-insensitive). Empty input, EOF, and any other value return false.
func Confirm(msg string, stdin io.Reader, stdout io.Writer) (bool, error) {
	_, err := fmt.Fprintf(stdout, "%s [y/N]: ", msg)
	if err != nil {
		return false, err
	}
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		// EOF or read error — treat as "no"
		if scanErr := scanner.Err(); scanErr != nil {
			return false, scanErr
		}
		return false, nil
	}
	answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return answer == "y" || answer == "yes", nil
}
