// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package prompt

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfirm(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"y", "y\n", true},
		{"Y", "Y\n", true},
		{"yes", "yes\n", true},
		{"YES", "YES\n", true},
		{"n", "n\n", false},
		{"no", "no\n", false},
		{"empty", "\n", false},
		{"eof", "", false},
		{"whitespace_y", "  y  \n", true},
		{"other", "maybe\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := Confirm("Proceed?", strings.NewReader(tt.input), &out)
			if err != nil {
				t.Fatalf("Confirm returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Confirm(input=%q) = %v, want %v", tt.input, got, tt.want)
			}
			if !strings.Contains(out.String(), "[y/N]") {
				t.Errorf("output missing prompt: %q", out.String())
			}
		})
	}
}
