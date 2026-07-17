// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package devcontainer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsBlockedBindSource(t *testing.T) {
	type bindSourceCase struct {
		name          string
		skipOnWindows bool
		wantBlocked   bool
		setup         func(t *testing.T) (source, projectDir string)
	}

	cases := []bindSourceCase{
		{
			name:        "trailing slash system dir",
			wantBlocked: true,
			setup: func(t *testing.T) (source, projectDir string) {
				return "/etc/", t.TempDir()
			},
		},
		{
			name:        "literal root",
			wantBlocked: true,
			setup: func(t *testing.T) (source, projectDir string) {
				return "/", t.TempDir()
			},
		},
		{
			name:        "traversal cleans to system dir",
			wantBlocked: true,
			setup: func(t *testing.T) (source, projectDir string) {
				return "/foo/../etc", t.TempDir()
			},
		},
		{
			name: "project subdir allowed",
			setup: func(t *testing.T) (source, projectDir string) {
				projectDir = t.TempDir()
				source = filepath.Join(projectDir, "data")
				if err := os.Mkdir(source, 0o755); err != nil {
					t.Fatalf("failed to create subdir: %v", err)
				}
				return source, projectDir
			},
		},
		{
			name: "project dir itself allowed",
			setup: func(t *testing.T) (source, projectDir string) {
				projectDir = t.TempDir()
				return projectDir, projectDir
			},
		},
		{
			name: "non-existent path under project allowed unresolved",
			setup: func(t *testing.T) (source, projectDir string) {
				projectDir = t.TempDir()
				return filepath.Join(projectDir, "will-be-created-by-docker"), projectDir
			},
		},
		{
			name:        "non-existent path under system dir still blocked",
			wantBlocked: true,
			setup: func(t *testing.T) (source, projectDir string) {
				return "/etc/does-not-exist-enclave-test", t.TempDir()
			},
		},
		{
			name:        "traversal escaping project blocked",
			wantBlocked: true,
			setup: func(t *testing.T) (source, projectDir string) {
				projectDir = t.TempDir()
				parent := filepath.Dir(projectDir)
				return filepath.Join(projectDir, "..", filepath.Base(parent)), projectDir
			},
		},
		{
			name:        "home .ssh blocked",
			wantBlocked: true,
			setup: func(t *testing.T) (source, projectDir string) {
				home := t.TempDir()
				source = filepath.Join(home, ".ssh")
				if err := os.MkdirAll(source, 0o700); err != nil {
					t.Fatalf("failed to create fake .ssh: %v", err)
				}
				return source, t.TempDir()
			},
		},
		{
			name:        "home .aws blocked",
			wantBlocked: true,
			setup: func(t *testing.T) (source, projectDir string) {
				home := t.TempDir()
				source = filepath.Join(home, ".aws")
				if err := os.MkdirAll(source, 0o700); err != nil {
					t.Fatalf("failed to create fake .aws: %v", err)
				}
				return source, t.TempDir()
			},
		},
		{
			name:          "ssh-agent socket blocked",
			skipOnWindows: true,
			wantBlocked:   true,
			setup: func(t *testing.T) (source, projectDir string) {
				return "/tmp/ssh-XXXXXXX/agent.123", t.TempDir()
			},
		},
		{
			name:        "whole home blocked",
			wantBlocked: true,
			setup: func(t *testing.T) (source, projectDir string) {
				return t.TempDir(), t.TempDir()
			},
		},
		{
			name:        "docker socket blocked",
			wantBlocked: true,
			setup: func(t *testing.T) (source, projectDir string) {
				return "/var/run/docker.sock", t.TempDir()
			},
		},
		{
			name:          "in-project symlink escaping project blocked",
			skipOnWindows: true,
			wantBlocked:   true,
			setup: func(t *testing.T) (source, projectDir string) {
				projectDir = t.TempDir()
				outside := t.TempDir()
				source = filepath.Join(projectDir, "escape")
				if err := os.Symlink(outside, source); err != nil {
					t.Fatalf("failed to create symlink: %v", err)
				}
				return source, projectDir
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipOnWindows && runtime.GOOS == "windows" {
				t.Skip("path semantics are unix-specific")
			}
			source, projectDir := tc.setup(t)
			if got := isBlockedBindSource(source, projectDir); got != tc.wantBlocked {
				t.Fatalf("isBlockedBindSource(%q, %q) = %v, want %v", source, projectDir, got, tc.wantBlocked)
			}
		})
	}
}
