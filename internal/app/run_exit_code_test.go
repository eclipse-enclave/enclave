// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"fmt"
	"testing"

	"enclave/internal/backend"
)

func TestRunErrorExitCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "tool exit status", err: &backend.ExitError{Code: 3}, want: 3},
		{name: "interrupted start", err: fmt.Errorf("run: %w", backend.ErrInterrupted), want: exitCodeInterrupted},
		{name: "other failure", err: errors.New("boom"), want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runErrorExitCode(tc.err); got != tc.want {
				t.Fatalf("runErrorExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}
