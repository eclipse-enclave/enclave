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
	"enclave/internal/model"
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
    "claude": {"session_tint": "#111111", "host_env": {"HERDR_AGENT": "claude"}},
    "codex": {"session_tint": "#222222", "host_env": {"HERDR_AGENT": "codex"}}
  }
}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
}

// The ambient tool is claude, so a codex session must still resolve the codex
// color and host_env rather than the default tool's.
func TestAttachSessionRunOptionsFollowSessionTool(t *testing.T) {
	writeGlobalToolTints(t)
	session := backend.Session{Tool: "codex", ProjectDir: t.TempDir()}

	got := attachSessionRunOptions(session, model.RunOptions{SessionTint: "#111111", HostEnv: map[string]string{"HERDR_AGENT": "claude"}})
	if got.SessionTint != "#222222" || got.HostEnv["HERDR_AGENT"] != "codex" {
		t.Fatalf("expected the codex options, got tint %q host_env %v", got.SessionTint, got.HostEnv)
	}
}

func TestAttachSessionRunOptionsFallBack(t *testing.T) {
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
			got := attachSessionRunOptions(tc.session, model.RunOptions{SessionTint: "#333333", HostEnv: map[string]string{"HERDR_AGENT": "fallback"}})
			if got.SessionTint != "#333333" || got.HostEnv["HERDR_AGENT"] != "fallback" {
				t.Fatalf("expected the fallback options, got tint %q host_env %v", got.SessionTint, got.HostEnv)
			}
		})
	}
}
