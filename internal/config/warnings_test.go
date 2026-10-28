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
	"reflect"
	"testing"
)

func TestLoadDefaultsWarnings(t *testing.T) {
	cases := []struct {
		name    string
		global  string
		project string
		want    func(global, project string) []Warning
	}{
		{
			name:   "config path is a directory",
			global: "<dir>",
			want: func(global, _ string) []Warning {
				return []Warning{{Code: WarningConfigPathIsDirectory, Layer: "global", Path: global,
					Message: "Config path is a directory: " + global}}
			},
		},
		{
			name:    "invalid config JSON",
			project: `{`,
			want: func(_, project string) []Warning {
				return []Warning{{Code: WarningInvalidConfigJSON, Layer: "project", Path: project,
					Message: "Invalid config JSON at " + project + ": unexpected end of JSON input"}}
			},
		},
		{
			name:   "top-level host_config_paths",
			global: `{"host_config_paths":["settings.json"]}`,
			want: func(global, _ string) []Warning {
				return []Warning{{Code: WarningUnsupportedScope, Option: "host_config_paths", Layer: "global", Path: global,
					Message: `Ignoring "host_config_paths" in ` + global + ": this option is only supported under tool_overrides.<tool>"}}
			},
		},
		{
			name:   "invalid tool overrides",
			global: `{"tool_overrides":{"codex":{"tool":"claude"},"pi":{"host_config_paths":["/etc/x"]}}}`,
			want: func(global, _ string) []Warning {
				return []Warning{
					{Code: WarningInvalidToolOverride, Layer: "tool_override:codex", Path: global,
						Message: `Ignoring tool_overrides.codex in ` + global + `: field "tool" is not allowed in per-tool overrides`},
					{Code: WarningSuspiciousValue, Option: "host_config_paths", Layer: "tool_override:pi", Path: global,
						Message: `Suspicious tool_overrides.pi.host_config_paths entry "/etc/x" in ` + global + ": passthrough paths are relative to the tool config dir, not absolute host paths"},
				}
			},
		},
		{
			name:   "invalid pass_env and session_tint",
			global: `{"pass_env":["1BAD","GITHUB_TOKEN=ghp_secret"],"session_tint":"red","tool_overrides":{"codex":{"pass_env":["BAD-DASH"],"session_tint":"#2a0"}}}`,
			want: func(global, _ string) []Warning {
				return []Warning{
					{Code: WarningInvalidValue, Option: "pass_env", Layer: "global", Path: global,
						Message: `Ignoring pass_env entry "1BAD" in ` + global + ": not a valid environment variable name"},
					{Code: WarningInvalidValue, Option: "pass_env", Layer: "global", Path: global,
						Message: `Ignoring pass_env entry "GITHUB_TOKEN=…" in ` + global + ": not a valid environment variable name"},
					{Code: WarningInvalidValue, Option: "pass_env", Layer: "tool_override:codex", Path: global,
						Message: `Ignoring tool_overrides.codex.pass_env entry "BAD-DASH" in ` + global + ": not a valid environment variable name"},
					{Code: WarningInvalidValue, Option: "session_tint", Layer: "global", Path: global,
						Message: `Ignoring session_tint "red" in ` + global + ": expected an #rrggbb color"},
					{Code: WarningInvalidValue, Option: "session_tint", Layer: "tool_override:codex", Path: global,
						Message: `Ignoring tool_overrides.codex.session_tint "#2a0" in ` + global + ": expected an #rrggbb color"},
				}
			},
		},
		{
			name:    "project global-only keys",
			project: `{"tool":"codex","allow_domains":["proj.example"],"add_dirs":["/etc"],"auto_update":true,"tool_overrides":{"codex":{"yolo":true,"update_channel":"rolling"}}}`,
			want: func(_, project string) []Warning {
				return []Warning{
					{Code: WarningInvalidToolOverride, Option: "update_channel", Layer: "tool_override:codex", Path: project,
						Message: "Ignoring tool_overrides.codex.update_channel in " + project + ": binary updates are configured globally"},
					{Code: WarningProjectGuardrail, Option: "auto_update", Layer: "project", Path: project,
						Message: "Ignoring auto_update=true in " + project + ": binary updates are configured globally"},
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectDir, globalPath, projectPath := setupWarningConfigs(t, tc.global, tc.project)

			_, _, got, err := LoadDefaults(projectDir)
			if err != nil {
				t.Fatalf("LoadDefaults: %v", err)
			}
			want := tc.want(globalPath, projectPath)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("warnings mismatch\n got: %#v\nwant: %#v", got, want)
			}
		})
	}
}

func setupWarningConfigs(t *testing.T, global, project string) (projectDir, globalPath, projectPath string) {
	t.Helper()
	unsetXDGEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectDir = t.TempDir()

	globalPath, err := GlobalConfigPath()
	if err != nil {
		t.Fatalf("GlobalConfigPath: %v", err)
	}
	proj, err := ResolveProjectFromDir(projectDir)
	if err != nil {
		t.Fatalf("ResolveProjectFromDir: %v", err)
	}
	projectPath = HostProjectConfigJSONPath(home, proj.Hash)
	writeConfigFixture(t, globalPath, global)
	writeConfigFixture(t, projectPath, project)
	return projectDir, globalPath, projectPath
}

func writeConfigFixture(t *testing.T, path string, content string) {
	t.Helper()
	if content == "" {
		return
	}
	if content == "<dir>" {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
