// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSELinuxEnforce(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{"enforcing", "1\n", true},
		{"permissive", "0\n", false},
		{"enforcing no newline", "1", true},
		{"permissive no newline", "0", false},
		{"empty", "", false},
		{"unexpected value", "2\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "enforce")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := readSELinuxEnforce(path); got != tt.expected {
				t.Errorf("readSELinuxEnforce(%q) = %v, want %v", tt.content, got, tt.expected)
			}
		})
	}
}

func TestReadSELinuxEnforceMissingFile(t *testing.T) {
	if got := readSELinuxEnforce("/nonexistent/path/enforce"); got {
		t.Error("readSELinuxEnforce with missing file should return false")
	}
}
