// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWorktreeInheritanceSelectorOnlyReadsSelector(t *testing.T) {
	for _, tc := range []struct {
		name, data          string
		set, value, warning bool
	}{
		{"true", `{"worktree_inheritance":true}`, true, true, false},
		{"false and obsolete option", `{"worktree_inheritance":false,"image_mode":"removed"}`, true, false, false},
		{"unrelated invalid type", `{"tool":[],"worktree_inheritance":true}`, true, true, false},
		{"tool override is not a selector", `{"tool_overrides":{"tool":{"worktree_inheritance":false}}}`, false, false, false},
		{"invalid selector", `{"worktree_inheritance":"false"}`, false, false, true},
		{"invalid JSON", `{`, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			path := HostProjectConfigJSONPath(home, "checkout")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			value, warnings, err := LoadWorktreeInheritanceSelector(home, "checkout")
			if err != nil || (value != nil) != tc.set || value != nil && *value != tc.value || (len(warnings) > 0) != tc.warning {
				t.Fatalf("selector=%v warnings=%+v err=%v", value, warnings, err)
			}
		})
	}
}

func TestHasUserOverridesDoesNotReadEscapingConfigSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "selection.json")
	if err := os.WriteFile(outside, []byte(`{"worktree_inheritance":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, configFilename)); err != nil {
		t.Fatal(err)
	}
	if !HasUserOverrides(root) {
		t.Fatal("escaping config symlink was read and treated as selection-only")
	}
}

func TestWorktreeInheritanceIsNotToolSpecific(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"tool_overrides":{"tool":{"worktree_inheritance":false,"no_cache":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	defaults, warnings, err := readDefaults(path)
	if err != nil {
		t.Fatal(err)
	}
	override := defaults.ToolOverrides["tool"]
	if override.WorktreeInheritance != nil || override.NoCache == nil || !*override.NoCache || len(warnings) != 1 || warnings[0].Option != "worktree_inheritance" {
		t.Fatalf("override=%+v warnings=%+v", override, warnings)
	}
}
