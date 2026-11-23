// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecretsFileDisplay(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "global.env")
	if err := os.WriteFile(present, []byte("KEY=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.env")

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "unresolved home", path: "", want: "unavailable"},
		{name: "present", path: present, want: present},
		{name: "missing", path: missing, want: missing + " (missing)"},
		{name: "directory", path: dir, want: dir + " (not a file)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := secretsFileDisplay(tt.path); got != tt.want {
				t.Errorf("secretsFileDisplay(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
