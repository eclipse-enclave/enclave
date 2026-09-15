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

	"enclave/internal/backend"
	"enclave/internal/config"
)

func writeGlobalToolTints(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path, err := config.GlobalConfigPath()
	if err != nil {
		t.Fatalf("resolve global config path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}
	payload := `{
  "tool": "claude",
  "tool_overrides": {
    "claude": {"session_tint": "#111111"},
    "codex": {"session_tint": "#222222"}
  }
}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
}

// The ambient tool is claude, so a codex session must still resolve the codex
// color rather than the default tool's.
func TestAttachSessionTintFollowsSessionTool(t *testing.T) {
	writeGlobalToolTints(t)
	session := backend.Session{Tool: "codex", ProjectDir: t.TempDir()}

	if got := attachSessionTint(session, "#111111"); got != "#222222" {
		t.Fatalf("expected the codex tint, got %q", got)
	}
}

func TestAttachSessionTintFallsBack(t *testing.T) {
	writeGlobalToolTints(t)

	cases := []struct {
		name    string
		session backend.Session
	}{
		{name: "unlabeled tool", session: backend.Session{ProjectDir: t.TempDir()}},
		{name: "unlabeled project dir", session: backend.Session{Tool: "codex"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := attachSessionTint(tc.session, "#333333"); got != "#333333" {
				t.Fatalf("expected the resolved fallback tint, got %q", got)
			}
		})
	}
}
