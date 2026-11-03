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
	"slices"
	"strings"
	"testing"
)

func TestDisabledCommandsReadsGlobalConfig(t *testing.T) {
	home := t.TempDir()
	// Pin the config root under the temp home: a host XDG_CONFIG_HOME would
	// otherwise point this test at the real global config.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path := filepath.Join(hostConfigRoot(home), configFilename)
	if !strings.HasPrefix(path, home) {
		t.Fatalf("config path %q escapes the temp home", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"disabled_commands": ["usage", "report"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	names, configPath := DisabledCommands(home)
	if configPath != path || !slices.Equal(names, []string{"usage", "report"}) {
		t.Fatalf("DisabledCommands = %v, %q", names, configPath)
	}

	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if names, _ := DisabledCommands(home); len(names) != 0 {
		t.Fatalf("a broken config disabled %v", names)
	}
}

// Command discovery only reads global config, so a project or tool-override
// value could never take effect.
func TestDisabledCommandsIsGlobalOnly(t *testing.T) {
	project := Defaults{DisabledCommands: []string{"usage"}}
	warnings := stripGlobalOnlyKeys("/tmp/projects/abc/config.json", &project)
	if project.DisabledCommands != nil || len(warnings) != 1 || !strings.Contains(warnings[0].Message, "disabled_commands") {
		t.Fatalf("project value kept: %+v, %v", project, warnings)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"tool_overrides": {"codex": {"disabled_commands": ["usage"], "yolo": false}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	defaults, warnings, err := readDefaults(path)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.ToolOverrides["codex"].DisabledCommands != nil || len(warnings) != 1 ||
		!strings.Contains(warnings[0].Message, "tool_overrides.codex.disabled_commands") {
		t.Fatalf("tool override value kept: %+v, %v", defaults.ToolOverrides, warnings)
	}
}
