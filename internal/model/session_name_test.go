// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package model

import "testing"

func TestSanitizeSessionName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"my-task", "my-task"},
		{"  My Task  ", "my-task"},
		{"feature/ABC-123", "feature-abc-123"},
		{"--task--", "task"},
		{"", ""},
		{"///", ""},
		{"a-very-long-session-name-that-exceeds-the-limit", "a-very-long-session-name-that-ex"},
		{"trailing-separator-after-truncation-x", "trailing-separator-after-truncat"},
	}
	for _, tt := range tests {
		if got := SanitizeSessionName(tt.in); got != tt.want {
			t.Fatalf("SanitizeSessionName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
