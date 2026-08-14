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
	"strings"
	"testing"

	"enclave/internal/model"
)

func TestToolOverride_SingleToolApplied(t *testing.T) {
	sources := model.DefaultOptionSources()
	opts := DefaultOptions()

	project := Defaults{
		ToolOverrides: map[string]Defaults{
			"pi": {NoAPIKey: boolPtr(true)},
		},
	}

	override, ok := ResolveToolOverrideDefaults(Defaults{}, project, "pi")
	if !ok {
		t.Fatalf("expected tool override for pi")
	}

	opts = ApplyDefaultsWithSources(opts, override, model.SourceToolOverride, &sources)
	if !opts.NoAPIKey {
		t.Fatalf("expected no_api_key to be set by tool override")
	}
	if sources.NoAPIKey != model.SourceToolOverride {
		t.Fatalf("expected no_api_key source=%v, got %v", model.SourceToolOverride, sources.NoAPIKey)
	}
}

func TestToolOverride_MultipleToolsOnlyAffectSelectedTool(t *testing.T) {
	sources := model.DefaultOptionSources()
	opts := DefaultOptions()

	project := Defaults{
		ToolOverrides: map[string]Defaults{
			"pi":    {NoAPIKey: boolPtr(true)},
			"codex": {AllowAllNetwork: boolPtr(true)},
		},
	}

	override, ok := ResolveToolOverrideDefaults(Defaults{}, project, "pi")
	if !ok {
		t.Fatalf("expected tool override for pi")
	}

	opts = ApplyDefaultsWithSources(opts, override, model.SourceToolOverride, &sources)
	if !opts.NoAPIKey {
		t.Fatalf("expected no_api_key to be set for selected tool")
	}
	if opts.AllowAllNetwork {
		t.Fatalf("allow_all_network should not be set from non-selected tool override")
	}
}

func TestToolOverride_ProjectWinsOverGlobal(t *testing.T) {
	global := Defaults{
		ToolOverrides: map[string]Defaults{
			"pi": {NoAPIKey: boolPtr(false)},
		},
	}
	project := Defaults{
		ToolOverrides: map[string]Defaults{
			"pi": {NoAPIKey: boolPtr(true)},
		},
	}

	override, ok := ResolveToolOverrideDefaults(global, project, "pi")
	if !ok {
		t.Fatalf("expected tool override for pi")
	}
	if override.NoAPIKey == nil || !*override.NoAPIKey {
		t.Fatalf("expected project tool override to win over global")
	}
}

func TestToolOverride_ClearsNestedToolOverrides(t *testing.T) {
	global := Defaults{
		ToolOverrides: map[string]Defaults{
			"pi": {
				NoAPIKey:      boolPtr(false),
				ToolOverrides: map[string]Defaults{"nested": {NoAPIKey: boolPtr(true)}},
			},
		},
	}
	project := Defaults{
		ToolOverrides: map[string]Defaults{
			"pi": {NoAPIKey: boolPtr(true)},
		},
	}

	override, ok := ResolveToolOverrideDefaults(global, project, "pi")
	if !ok {
		t.Fatalf("expected tool override for pi")
	}
	if override.ToolOverrides != nil {
		t.Fatalf("expected nested tool_overrides to be cleared")
	}
}

func TestToolOverride_CLIWinsOverToolOverride(t *testing.T) {
	sources := model.DefaultOptionSources()
	opts := DefaultOptions()
	opts.NoAPIKey = true
	sources.NoAPIKey = model.SourceCLI

	override := Defaults{NoAPIKey: boolPtr(false)}
	opts = ApplyDefaultsWithSources(opts, override, model.SourceToolOverride, &sources)

	if !opts.NoAPIKey {
		t.Fatalf("tool override should not override CLI value")
	}
	if sources.NoAPIKey != model.SourceCLI {
		t.Fatalf("expected CLI source to remain unchanged, got %v", sources.NoAPIKey)
	}
}

func TestReadDefaults_InvalidToolOverrideEntriesIgnoredWithWarnings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	payload := `{
  "tool_overrides": {
    "pi": {
      "tool": "codex",
      "no_api_key": true
    },
    "opencode": {
      "tool_overrides": {
        "nested": {
          "no_api_key": true
        }
      }
    },
    "codex": {
      "no_api_key": true
    }
  }
}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	defaults, warnings, err := readDefaults(path)
	if err != nil {
		t.Fatalf("readDefaults returned error: %v", err)
	}

	if len(defaults.ToolOverrides) != 1 {
		t.Fatalf("expected exactly one valid tool override, got %d", len(defaults.ToolOverrides))
	}
	if _, ok := defaults.ToolOverrides["codex"]; !ok {
		t.Fatalf("expected codex override to remain after validation")
	}
	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d: %v", len(warnings), warnings)
	}
	if !containsSubstring(warnings, "tool_overrides.pi") {
		t.Fatalf("expected warning for pi override, warnings=%v", warnings)
	}
	if !containsSubstring(warnings, "tool_overrides.opencode") {
		t.Fatalf("expected warning for opencode override, warnings=%v", warnings)
	}
}

func TestReadDefaults_IgnoresTopLevelHostConfigPathsButKeepsToolOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	payload := `{
  "host_config_paths": ["default", "+statusbar/"],
  "tool_overrides": {
    "claude": {
      "host_config_paths": ["-commands/", "+statusbar/"]
    }
  }
}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	defaults, warnings, err := readDefaults(path)
	if err != nil {
		t.Fatalf("readDefaults returned error: %v", err)
	}
	if defaults.HostConfigPaths != nil {
		t.Fatalf("expected top-level host_config_paths to be ignored, got %v", defaults.HostConfigPaths)
	}
	override, ok := defaults.ToolOverrides["claude"]
	if !ok {
		t.Fatalf("expected claude tool override to remain")
	}
	want := []string{"-commands/", "+statusbar/"}
	if !reflect.DeepEqual(override.HostConfigPaths, want) {
		t.Fatalf("unexpected tool override host_config_paths: got %v want %v", override.HostConfigPaths, want)
	}
	if !containsSubstring(warnings, "host_config_paths") {
		t.Fatalf("expected warning for top-level host_config_paths, warnings=%v", warnings)
	}
}

func TestReadDefaults_WarnsOnAbsoluteToolOverrideHostConfigPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	payload := `{
  "tool_overrides": {
    "claude": {
      "host_config_paths": ["default", "+/home/user/.claude/statusline-command.sh"]
    }
  }
}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	defaults, warnings, err := readDefaults(path)
	if err != nil {
		t.Fatalf("readDefaults returned error: %v", err)
	}

	override, ok := defaults.ToolOverrides["claude"]
	if !ok {
		t.Fatalf("expected claude tool override to remain")
	}
	want := []string{"default", "+/home/user/.claude/statusline-command.sh"}
	if !reflect.DeepEqual(override.HostConfigPaths, want) {
		t.Fatalf("unexpected tool override host_config_paths: got %v want %v", override.HostConfigPaths, want)
	}
	if !containsSubstring(warnings, "relative to the tool config dir") {
		t.Fatalf("expected warning for absolute host_config_paths entry, warnings=%v", warnings)
	}
}

func TestProjectOverrideGuardrails_BlockAllowAllNetworkElevation(t *testing.T) {
	defaults := Defaults{
		AllowAllNetwork: boolPtr(true),
		ToolOverrides: map[string]Defaults{
			"codex": {AllowAllNetwork: boolPtr(true), NoAPIKey: boolPtr(true)},
			"pi":    {AllowAllNetwork: boolPtr(false)},
		},
	}

	warnings := applyProjectOverrideGuardrails("/tmp/project/.enclave/config.json", "/tmp/project", &defaults)
	if len(warnings) != 2 {
		t.Fatalf("expected 2 guardrail warnings, got %d: %v", len(warnings), warnings)
	}
	if defaults.AllowAllNetwork != nil {
		t.Fatalf("expected project-level allow_all_network=true to be ignored")
	}

	codex := defaults.ToolOverrides["codex"]
	if codex.AllowAllNetwork != nil {
		t.Fatalf("expected tool override allow_all_network=true to be ignored")
	}
	if codex.NoAPIKey == nil || !*codex.NoAPIKey {
		t.Fatalf("expected non-guarded fields to remain unchanged")
	}

	pi := defaults.ToolOverrides["pi"]
	if pi.AllowAllNetwork == nil || *pi.AllowAllNetwork {
		t.Fatalf("expected allow_all_network=false to remain set for project overrides")
	}
	if !containsSubstring(warnings, "\"allow_all_network\"=true") {
		t.Fatalf("expected project warning for allow_all_network=true, warnings=%v", warnings)
	}
	if !containsSubstring(warnings, "tool_overrides.codex.allow_all_network=true") {
		t.Fatalf("expected tool override warning for codex, warnings=%v", warnings)
	}
}

func TestToolOverride_ProjectGuardrailAllowsGlobalElevation(t *testing.T) {
	global := Defaults{
		ToolOverrides: map[string]Defaults{
			"codex": {AllowAllNetwork: boolPtr(true)},
		},
	}
	project := Defaults{
		ToolOverrides: map[string]Defaults{
			"codex": {AllowAllNetwork: boolPtr(true)},
		},
	}

	warnings := applyProjectOverrideGuardrails("/tmp/project/.enclave/config.json", "/tmp/project", &project)
	if len(warnings) != 1 {
		t.Fatalf("expected one guardrail warning, got %d: %v", len(warnings), warnings)
	}

	override, ok := ResolveToolOverrideDefaults(global, project, "codex")
	if !ok {
		t.Fatalf("expected tool override to resolve")
	}
	if override.AllowAllNetwork == nil || !*override.AllowAllNetwork {
		t.Fatalf("expected global allow_all_network=true to remain effective after project guardrail filtering")
	}
}

func TestProjectOverrideGuardrails_WorktreeMetadataCannotRelaxInheritedNone(t *testing.T) {
	global := Defaults{
		WorktreeMetadata: model.WorktreeMetadataNone,
		ToolOverrides: map[string]Defaults{
			"claude": {WorktreeMetadata: model.WorktreeMetadataNone},
			"codex":  {WorktreeMetadata: model.WorktreeMetadataFollow},
		},
	}
	project := Defaults{
		WorktreeMetadata: model.WorktreeMetadataReadonly,
		ToolOverrides: map[string]Defaults{
			"claude": {WorktreeMetadata: model.WorktreeMetadataReadonly},
			"codex":  {WorktreeMetadata: model.WorktreeMetadataReadonly},
		},
	}

	warnings := applyProjectOverrideGuardrailsAgainst("/tmp/project/.enclave/config.json", "/tmp/project", global, &project)
	if len(warnings) != 2 {
		t.Fatalf("expected two guardrail warnings, got %d: %v", len(warnings), warnings)
	}
	if project.WorktreeMetadata != "" {
		t.Fatalf("expected top-level readonly override of none to be cleared, got %q", project.WorktreeMetadata)
	}
	if got := project.ToolOverrides["claude"].WorktreeMetadata; got != "" {
		t.Fatalf("expected claude readonly override of none to be cleared, got %q", got)
	}
	if got := project.ToolOverrides["codex"].WorktreeMetadata; got != model.WorktreeMetadataReadonly {
		t.Fatalf("expected codex readonly to strengthen its global follow override, got %q", got)
	}
	if !containsSubstring(warnings, "inherited worktree_metadata=\"none\"") {
		t.Fatalf("expected inherited none warning, got %v", warnings)
	}
}

func TestProjectOverrideGuardrails_AddDirContainment(t *testing.T) {
	type addDirCase struct {
		name         string
		warningCount int
		warnings     []string
		setup        func(t *testing.T) (configPath string, defaults Defaults)
		assert       func(t *testing.T, defaults Defaults)
	}

	cases := []addDirCase{
		{
			name:         "strips add_dirs outside project",
			warningCount: 2,
			warnings:     []string{"add_dirs entry \"~/.aws\"", "add_dirs entry \"/etc\""},
			setup: func(t *testing.T) (string, Defaults) {
				return projectConfigPathForTest(t), Defaults{AddDirs: []string{"~/.aws", "/etc"}}
			},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.AddDirs != nil {
					t.Fatalf("expected add_dirs entries outside project to be dropped, got %v", defaults.AddDirs)
				}
			},
		},
		{
			name:         "strips empty add_dirs entry",
			warningCount: 1,
			warnings:     []string{"add_dirs entry \"\""},
			setup: func(t *testing.T) (string, Defaults) {
				return projectConfigPathForTest(t), Defaults{AddDirs: []string{""}}
			},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.AddDirs != nil {
					t.Fatalf("expected empty add_dirs entry to be dropped, got %v", defaults.AddDirs)
				}
			},
		},
		{
			name:         "strips add_dirs sibling escape",
			warningCount: 1,
			warnings:     []string{"add_dirs entry \"../sibling/path\""},
			setup: func(t *testing.T) (string, Defaults) {
				return projectConfigPathForTest(t), Defaults{AddDirs: []string{"../sibling/path"}}
			},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.AddDirs != nil {
					t.Fatalf("expected sibling-escape add_dirs entry to be dropped, got %v", defaults.AddDirs)
				}
			},
		},
		{
			name:         "keeps add_dirs inside project",
			warningCount: 0,
			setup: func(t *testing.T) (string, Defaults) {
				projectDir := t.TempDir()
				dataDir := filepath.Join(projectDir, "data")
				if err := os.MkdirAll(dataDir, 0o755); err != nil {
					t.Fatalf("mkdir data: %v", err)
				}
				return filepath.Join(projectDir, ".enclave", "config.json"), Defaults{AddDirs: []string{"./data"}}
			},
			assert: func(t *testing.T, defaults Defaults) {
				if !reflect.DeepEqual(defaults.AddDirs, []string{"./data"}) {
					t.Fatalf("expected ./data to be kept, got %v", defaults.AddDirs)
				}
			},
		},
		{
			name:         "strips add_readonly_dirs outside project",
			warningCount: 1,
			warnings:     []string{"add_readonly_dirs entry \"/etc\""},
			setup: func(t *testing.T) (string, Defaults) {
				return projectConfigPathForTest(t), Defaults{AddReadonlyDirs: []string{"/etc"}}
			},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.AddReadonlyDirs != nil {
					t.Fatalf("expected add_readonly_dirs entries outside project to be dropped, got %v", defaults.AddReadonlyDirs)
				}
			},
		},
		{
			name:         "strips tool override add_dirs outside project",
			warningCount: skipWarningCountCheck,
			warnings:     []string{"tool_overrides.codex.add_dirs entry \"/etc\""},
			setup: func(t *testing.T) (string, Defaults) {
				return projectConfigPathForTest(t), Defaults{ToolOverrides: map[string]Defaults{
					"codex": {AddDirs: []string{"/etc"}, NoAPIKey: boolPtr(true)},
				}}
			},
			assert: func(t *testing.T, defaults Defaults) {
				codex := defaults.ToolOverrides["codex"]
				if codex.AddDirs != nil {
					t.Fatalf("expected tool override add_dirs to be dropped, got %v", codex.AddDirs)
				}
				assertNoAPIKeyPreserved(t, codex)
			},
		},
		{
			name:         "strips symlink escape",
			warningCount: skipWarningCountCheck,
			warnings:     []string{"add_dirs entry \"./escape\""},
			setup: func(t *testing.T) (string, Defaults) {
				projectDir, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatalf("eval project tempdir: %v", err)
				}
				outsideDir, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatalf("eval outside tempdir: %v", err)
				}
				linkPath := filepath.Join(projectDir, "escape")
				if err := os.Symlink(outsideDir, linkPath); err != nil {
					t.Fatalf("create symlink: %v", err)
				}
				return filepath.Join(projectDir, ".enclave", "config.json"), Defaults{AddDirs: []string{"./escape"}}
			},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.AddDirs != nil {
					t.Fatalf("expected symlinked-out add_dirs entry to be dropped, got %v", defaults.AddDirs)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath, defaults := tc.setup(t)
			projectDir := filepath.Dir(filepath.Dir(configPath))
			warnings := applyProjectOverrideGuardrails(configPath, projectDir, &defaults)
			assertWarnings(t, warnings, tc.warningCount, tc.warnings...)
			tc.assert(t, defaults)
		})
	}
}

func TestProjectOverrideGuardrails_AllowAllNetworkRegression(t *testing.T) {
	projectDir := t.TempDir()
	configPath := filepath.Join(projectDir, ".enclave", "config.json")

	defaults := Defaults{
		AllowAllNetwork: boolPtr(true),
		ToolOverrides: map[string]Defaults{
			"codex": {AllowAllNetwork: boolPtr(true)},
		},
	}

	warnings := applyProjectOverrideGuardrails(configPath, projectDir, &defaults)
	if defaults.AllowAllNetwork != nil {
		t.Fatalf("expected project-level allow_all_network=true to be ignored")
	}
	if defaults.ToolOverrides["codex"].AllowAllNetwork != nil {
		t.Fatalf("expected tool override allow_all_network=true to be ignored")
	}
	if !containsSubstring(warnings, "\"allow_all_network\"=true") {
		t.Fatalf("expected project warning for allow_all_network, warnings=%v", warnings)
	}
	if !containsSubstring(warnings, "tool_overrides.codex.allow_all_network=true") {
		t.Fatalf("expected tool override warning for codex, warnings=%v", warnings)
	}
}

func TestProjectOverrideGuardrails_StripsPassEnv(t *testing.T) {
	defaults := Defaults{
		PassEnv: []string{"AWS_SECRET_ACCESS_KEY"},
		ToolOverrides: map[string]Defaults{
			"codex": {PassEnv: []string{"FOO"}, NoAPIKey: boolPtr(true)},
		},
	}

	warnings := applyProjectOverrideGuardrails("/tmp/project/.enclave/config.json", "/tmp/project", &defaults)
	if len(warnings) != 2 {
		t.Fatalf("expected 2 guardrail warnings, got %d: %v", len(warnings), warnings)
	}
	if defaults.PassEnv != nil {
		t.Fatalf("expected project pass_env to be cleared, got %v", defaults.PassEnv)
	}
	codex := defaults.ToolOverrides["codex"]
	if codex.PassEnv != nil {
		t.Fatalf("expected tool_overrides.codex.pass_env to be cleared, got %v", codex.PassEnv)
	}
	if codex.NoAPIKey == nil || !*codex.NoAPIKey {
		t.Fatalf("expected non-guarded fields to remain unchanged")
	}
	if !containsSubstring(warnings, "Ignoring pass_env in") {
		t.Fatalf("expected project pass_env warning, warnings=%v", warnings)
	}
	if !containsSubstring(warnings, "tool_overrides.codex.pass_env") {
		t.Fatalf("expected tool override pass_env warning, warnings=%v", warnings)
	}
}

func TestReadDefaults_GlobalPassEnvPreservedWhenValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	payload := `{"pass_env": ["VALID_NAME", "ANOTHER_OK"]}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	defaults, warnings, err := readDefaults(path)
	if err != nil {
		t.Fatalf("readDefaults returned error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for valid pass_env, got %v", warnings)
	}
	if !reflect.DeepEqual(defaults.PassEnv, []string{"VALID_NAME", "ANOTHER_OK"}) {
		t.Fatalf("expected pass_env preserved, got %v", defaults.PassEnv)
	}
}

func TestReadDefaults_GlobalPassEnvDropsInvalidEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	payload := `{
  "pass_env": ["1BAD", "FOO BAR", "VALID_NAME"],
  "tool_overrides": {
    "codex": {
      "pass_env": ["OK_NAME", "BAD-DASH"]
    }
  }
}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	defaults, warnings, err := readDefaults(path)
	if err != nil {
		t.Fatalf("readDefaults returned error: %v", err)
	}
	if !reflect.DeepEqual(defaults.PassEnv, []string{"VALID_NAME"}) {
		t.Fatalf("expected only VALID_NAME to remain, got %v", defaults.PassEnv)
	}
	codex, ok := defaults.ToolOverrides["codex"]
	if !ok {
		t.Fatalf("expected codex tool override to remain")
	}
	if !reflect.DeepEqual(codex.PassEnv, []string{"OK_NAME"}) {
		t.Fatalf("expected only OK_NAME to remain in codex override, got %v", codex.PassEnv)
	}
	if !containsSubstring(warnings, "\"1BAD\"") {
		t.Fatalf("expected warning for 1BAD, warnings=%v", warnings)
	}
	if !containsSubstring(warnings, "\"FOO BAR\"") {
		t.Fatalf("expected warning for \"FOO BAR\", warnings=%v", warnings)
	}
	if !containsSubstring(warnings, "tool_overrides.codex.pass_env entry \"BAD-DASH\"") {
		t.Fatalf("expected tool override warning for BAD-DASH, warnings=%v", warnings)
	}
	if containsSubstring(warnings, "VALID_NAME") {
		t.Fatalf("did not expect VALID_NAME in warnings: %v", warnings)
	}
	if containsSubstring(warnings, "\"OK_NAME\"") {
		t.Fatalf("did not expect OK_NAME in warnings: %v", warnings)
	}
}

func TestProjectOverrideGuardrails_StripsSensitiveFields(t *testing.T) {
	cases := []struct {
		name         string
		defaults     Defaults
		warnings     []string
		warningCount int
		assert       func(t *testing.T, defaults Defaults)
	}{
		{
			name: "base_image",
			defaults: Defaults{
				BaseImage: "attacker.example.com/evil:latest",
				ToolOverrides: map[string]Defaults{
					"codex": {BaseImage: "attacker.example.com/codex-evil:latest", NoAPIKey: boolPtr(true)},
					"pi":    {NoAPIKey: boolPtr(true)},
				},
			},
			warningCount: 2,
			warnings: []string{
				"Ignoring base_image=\"attacker.example.com/evil:latest\" in",
				"tool_overrides.codex.base_image=\"attacker.example.com/codex-evil:latest\"",
			},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.BaseImage != "" {
					t.Fatalf("expected project base_image to be cleared, got %q", defaults.BaseImage)
				}
				codex := defaults.ToolOverrides["codex"]
				if codex.BaseImage != "" {
					t.Fatalf("expected tool_overrides.codex.base_image to be cleared, got %q", codex.BaseImage)
				}
				assertNoAPIKeyPreserved(t, codex)
			},
		},
		{
			name: "bridge_ports",
			defaults: Defaults{
				BridgePorts: []string{"22", "5432"},
				ToolOverrides: map[string]Defaults{
					"claude": {BridgePorts: []string{"6379"}, NoAPIKey: boolPtr(true)},
					"pi":     {NoAPIKey: boolPtr(true)},
				},
			},
			warningCount: 2,
			warnings: []string{
				"Ignoring bridge_ports=[22 5432] in",
				"tool_overrides.claude.bridge_ports=[6379]",
			},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.BridgePorts != nil {
					t.Fatalf("expected project bridge_ports to be cleared, got %v", defaults.BridgePorts)
				}
				claude := defaults.ToolOverrides["claude"]
				if claude.BridgePorts != nil {
					t.Fatalf("expected tool_overrides.claude.bridge_ports to be cleared, got %v", claude.BridgePorts)
				}
				assertNoAPIKeyPreserved(t, claude)
			},
		},
		{
			name: "allow_domains",
			defaults: Defaults{
				AllowDomains: []string{"evil-c2.example.com", "exfil.example.net"},
				ToolOverrides: map[string]Defaults{
					"claude": {AllowDomains: []string{"attacker.example.org"}, NoAPIKey: boolPtr(true)},
					"pi":     {NoAPIKey: boolPtr(true)},
				},
			},
			warningCount: 2,
			warnings: []string{
				"Ignoring allow_domains=[evil-c2.example.com exfil.example.net] in",
				"tool_overrides.claude.allow_domains=[attacker.example.org]",
			},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.AllowDomains != nil {
					t.Fatalf("expected project allow_domains to be cleared, got %v", defaults.AllowDomains)
				}
				claude := defaults.ToolOverrides["claude"]
				if claude.AllowDomains != nil {
					t.Fatalf("expected tool_overrides.claude.allow_domains to be cleared, got %v", claude.AllowDomains)
				}
				assertNoAPIKeyPreserved(t, claude)
			},
		},
		{
			name:         "top-level host_config",
			defaults:     Defaults{HostConfig: "passthrough"},
			warningCount: 1,
			warnings:     []string{"Ignoring host_config=\"passthrough\" in"},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.HostConfig != "" {
					t.Fatalf("expected project host_config to be cleared, got %q", defaults.HostConfig)
				}
			},
		},
		{
			name:         "top-level host_config_paths",
			defaults:     Defaults{HostConfigPaths: []string{"+settings.json"}},
			warningCount: 1,
			warnings:     []string{"Ignoring host_config_paths=[+settings.json] in"},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.HostConfigPaths != nil {
					t.Fatalf("expected project host_config_paths to be cleared, got %v", defaults.HostConfigPaths)
				}
			},
		},
		{
			name: "tool override host_config",
			defaults: Defaults{ToolOverrides: map[string]Defaults{
				"claude": {HostConfig: "passthrough", NoAPIKey: boolPtr(true)},
			}},
			warningCount: 1,
			warnings:     []string{"tool_overrides.claude.host_config=\"passthrough\""},
			assert: func(t *testing.T, defaults Defaults) {
				claude := defaults.ToolOverrides["claude"]
				if claude.HostConfig != "" {
					t.Fatalf("expected tool_overrides.claude.host_config to be cleared, got %q", claude.HostConfig)
				}
				assertNoAPIKeyPreserved(t, claude)
			},
		},
		{
			name: "tool override host_config_paths",
			defaults: Defaults{ToolOverrides: map[string]Defaults{
				"claude": {HostConfigPaths: []string{"+settings.json"}, NoAPIKey: boolPtr(true)},
			}},
			warningCount: 1,
			warnings:     []string{"tool_overrides.claude.host_config_paths=[+settings.json]"},
			assert: func(t *testing.T, defaults Defaults) {
				claude := defaults.ToolOverrides["claude"]
				if claude.HostConfigPaths != nil {
					t.Fatalf("expected tool_overrides.claude.host_config_paths to be cleared, got %v", claude.HostConfigPaths)
				}
				assertNoAPIKeyPreserved(t, claude)
			},
		},
		{
			name:         "top-level project_mount writable",
			defaults:     Defaults{ProjectMount: model.ProjectMountWritable},
			warningCount: 1,
			warnings:     []string{"Ignoring project_mount=\"writable\" in"},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.ProjectMount != "" {
					t.Fatalf("expected project_mount to be cleared, got %q", defaults.ProjectMount)
				}
			},
		},
		{
			name: "tool override project_mount writable",
			defaults: Defaults{ToolOverrides: map[string]Defaults{
				"claude": {ProjectMount: model.ProjectMountWritable, NoAPIKey: boolPtr(true)},
			}},
			warningCount: 1,
			warnings:     []string{"tool_overrides.claude.project_mount=\"writable\""},
			assert: func(t *testing.T, defaults Defaults) {
				claude := defaults.ToolOverrides["claude"]
				if claude.ProjectMount != "" {
					t.Fatalf("expected tool_overrides.claude.project_mount to be cleared, got %q", claude.ProjectMount)
				}
				assertNoAPIKeyPreserved(t, claude)
			},
		},
		{
			name: "project_mount readonly allowed",
			defaults: Defaults{
				ProjectMount: model.ProjectMountReadonly,
				ToolOverrides: map[string]Defaults{
					"claude": {ProjectMount: model.ProjectMountReadonly, NoAPIKey: boolPtr(true)},
				},
			},
			warningCount: 0,
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.ProjectMount != model.ProjectMountReadonly {
					t.Fatalf("expected top-level project_mount readonly, got %q", defaults.ProjectMount)
				}
				claude := defaults.ToolOverrides["claude"]
				if claude.ProjectMount != model.ProjectMountReadonly {
					t.Fatalf("expected tool_overrides.claude.project_mount readonly, got %q", claude.ProjectMount)
				}
				assertNoAPIKeyPreserved(t, claude)
			},
		},
		{
			name:         "top-level worktree_metadata follow",
			defaults:     Defaults{WorktreeMetadata: model.WorktreeMetadataFollow},
			warningCount: 1,
			warnings:     []string{"Ignoring worktree_metadata=\"follow\" in"},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.WorktreeMetadata != "" {
					t.Fatalf("expected worktree_metadata to be cleared, got %q", defaults.WorktreeMetadata)
				}
			},
		},
		{
			name: "tool override worktree_metadata follow",
			defaults: Defaults{ToolOverrides: map[string]Defaults{
				"claude": {WorktreeMetadata: model.WorktreeMetadataFollow, NoAPIKey: boolPtr(true)},
			}},
			warningCount: 1,
			warnings:     []string{"tool_overrides.claude.worktree_metadata=\"follow\""},
			assert: func(t *testing.T, defaults Defaults) {
				claude := defaults.ToolOverrides["claude"]
				if claude.WorktreeMetadata != "" {
					t.Fatalf("expected tool_overrides.claude.worktree_metadata to be cleared, got %q", claude.WorktreeMetadata)
				}
				assertNoAPIKeyPreserved(t, claude)
			},
		},
		{
			name: "worktree_metadata readonly and none allowed",
			defaults: Defaults{
				WorktreeMetadata: model.WorktreeMetadataReadonly,
				ToolOverrides: map[string]Defaults{
					"claude": {WorktreeMetadata: model.WorktreeMetadataNone, NoAPIKey: boolPtr(true)},
				},
			},
			warningCount: 0,
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.WorktreeMetadata != model.WorktreeMetadataReadonly {
					t.Fatalf("expected top-level worktree_metadata readonly, got %q", defaults.WorktreeMetadata)
				}
				claude := defaults.ToolOverrides["claude"]
				if claude.WorktreeMetadata != model.WorktreeMetadataNone {
					t.Fatalf("expected tool_overrides.claude.worktree_metadata none, got %q", claude.WorktreeMetadata)
				}
				assertNoAPIKeyPreserved(t, claude)
			},
		},
		{
			name:         "top-level tool",
			defaults:     Defaults{Tool: "codex"},
			warningCount: 1,
			warnings:     []string{"Ignoring tool=\"codex\" in"},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.Tool != "" {
					t.Fatalf("expected project tool to be cleared, got %q", defaults.Tool)
				}
			},
		},
		{
			name:         "top-level yolo true",
			defaults:     Defaults{Yolo: boolPtr(true)},
			warningCount: 1,
			warnings:     []string{"Ignoring yolo=true in"},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.Yolo != nil {
					t.Fatalf("expected project yolo to be cleared, got %v", *defaults.Yolo)
				}
			},
		},
		{
			name:         "top-level yolo false",
			defaults:     Defaults{Yolo: boolPtr(false)},
			warningCount: 1,
			warnings:     []string{"Ignoring yolo=false in"},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.Yolo != nil {
					t.Fatalf("expected project yolo to be cleared, got %v", *defaults.Yolo)
				}
			},
		},
		{
			name: "tool override yolo",
			defaults: Defaults{ToolOverrides: map[string]Defaults{
				"claude": {Yolo: boolPtr(true), NoAPIKey: boolPtr(true)},
			}},
			warningCount: 1,
			warnings:     []string{"tool_overrides.claude.yolo=true"},
			assert: func(t *testing.T, defaults Defaults) {
				claude := defaults.ToolOverrides["claude"]
				if claude.Yolo != nil {
					t.Fatalf("expected tool_overrides.claude.yolo to be cleared, got %v", *claude.Yolo)
				}
				assertNoAPIKeyPreserved(t, claude)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defaults, warnings := applyProjectGuardrailsForTest(tc.defaults)
			assertWarnings(t, warnings, tc.warningCount, tc.warnings...)
			tc.assert(t, defaults)
		})
	}
}

func TestProjectOverrideGuardrails_EmptySensitiveFieldsNoWarning(t *testing.T) {
	cases := []struct {
		name     string
		defaults Defaults
		assert   func(t *testing.T, defaults Defaults)
	}{
		{
			name: "base_image",
			defaults: Defaults{ToolOverrides: map[string]Defaults{
				"codex": {NoAPIKey: boolPtr(true)},
			}},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.BaseImage != "" {
					t.Fatalf("expected base_image to remain empty, got %q", defaults.BaseImage)
				}
				if defaults.ToolOverrides["codex"].BaseImage != "" {
					t.Fatalf("expected tool override base_image to remain empty")
				}
			},
		},
		{
			name: "bridge_ports",
			defaults: Defaults{ToolOverrides: map[string]Defaults{
				"claude": {NoAPIKey: boolPtr(true)},
			}},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.BridgePorts != nil {
					t.Fatalf("expected bridge_ports to remain nil, got %v", defaults.BridgePorts)
				}
				if defaults.ToolOverrides["claude"].BridgePorts != nil {
					t.Fatalf("expected tool override bridge_ports to remain nil")
				}
			},
		},
		{
			name: "allow_domains",
			defaults: Defaults{ToolOverrides: map[string]Defaults{
				"claude": {NoAPIKey: boolPtr(true)},
			}},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.AllowDomains != nil {
					t.Fatalf("expected allow_domains to remain nil, got %v", defaults.AllowDomains)
				}
				if defaults.ToolOverrides["claude"].AllowDomains != nil {
					t.Fatalf("expected tool override allow_domains to remain nil")
				}
			},
		},
		{
			name: "top-level host_config",
			defaults: Defaults{ToolOverrides: map[string]Defaults{
				"claude": {NoAPIKey: boolPtr(true)},
			}},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.HostConfig != "" {
					t.Fatalf("expected host_config to remain empty, got %q", defaults.HostConfig)
				}
			},
		},
		{
			name: "top-level tool",
			defaults: Defaults{ToolOverrides: map[string]Defaults{
				"codex": {NoAPIKey: boolPtr(true)},
			}},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.Tool != "" {
					t.Fatalf("expected tool to remain empty, got %q", defaults.Tool)
				}
			},
		},
		{
			name: "top-level yolo",
			defaults: Defaults{ToolOverrides: map[string]Defaults{
				"claude": {NoAPIKey: boolPtr(true)},
			}},
			assert: func(t *testing.T, defaults Defaults) {
				if defaults.Yolo != nil {
					t.Fatalf("expected yolo to remain nil, got %v", *defaults.Yolo)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defaults, warnings := applyProjectGuardrailsForTest(tc.defaults)
			assertWarnings(t, warnings, 0)
			tc.assert(t, defaults)
		})
	}
}

func TestReadDefaults_RejectsRemovedFields(t *testing.T) {
	cases := map[string]string{
		"image_mode":                        `{"image_mode":"all"}`,
		"agent_tools":                       `{"agent_tools":["claude","codex"]}`,
		"no_agents":                         `{"no_agents":true}`,
		"tool_overrides.claude.agent_tools": `{"tool_overrides":{"claude":{"agent_tools":["codex"]}}}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if _, _, err := readDefaults(path); err == nil {
				t.Fatalf("expected hard error for removed field %s", name)
			} else if !strings.Contains(err.Error(), "no longer supported") {
				t.Fatalf("unexpected error for %s: %v", name, err)
			}
		})
	}
}

func TestResolveOptionsForTool_AppliesTargetToolOverrides(t *testing.T) {
	global := Defaults{}
	project := Defaults{
		ToolOverrides: map[string]Defaults{
			"codex": {Slim: boolPtr(true)},
		},
	}
	cliOpts := DefaultOptions() // default tool is claude
	cliSources := model.DefaultOptionSources()

	// The default tool (claude) must not inherit codex's slim override.
	claudeOpts, _, _ := ResolveOptionsForTool(cliOpts, cliSources, global, project, "")
	if claudeOpts.Slim {
		t.Fatalf("default tool must not inherit codex slim override")
	}

	// An explicit codex target must resolve codex's own slim override, exactly
	// as `enclave --tool codex` would.
	codexOpts, _, ok := ResolveOptionsForTool(cliOpts, cliSources, global, project, "codex")
	if !ok {
		t.Fatalf("expected codex tool override to apply")
	}
	if !codexOpts.Slim {
		t.Fatalf("update codex must resolve codex slim override")
	}
	if codexOpts.Tool != "codex" {
		t.Fatalf("expected resolved tool codex, got %q", codexOpts.Tool)
	}
}

const skipWarningCountCheck = -1

func applyProjectGuardrailsForTest(defaults Defaults) (Defaults, []string) {
	warnings := applyProjectOverrideGuardrails("/tmp/project/.enclave/config.json", "/tmp/project", &defaults)
	return defaults, warnings
}

func projectConfigPathForTest(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), ".enclave", "config.json")
}

func assertWarnings(t *testing.T, warnings []string, wantCount int, wantSubstrings ...string) {
	t.Helper()
	if wantCount != skipWarningCountCheck && len(warnings) != wantCount {
		t.Fatalf("expected %d guardrail warnings, got %d: %v", wantCount, len(warnings), warnings)
	}
	for _, want := range wantSubstrings {
		if !containsSubstring(warnings, want) {
			t.Fatalf("expected warning containing %q, warnings=%v", want, warnings)
		}
	}
}

func assertNoAPIKeyPreserved(t *testing.T, defaults Defaults) {
	t.Helper()
	if defaults.NoAPIKey == nil || !*defaults.NoAPIKey {
		t.Fatalf("expected non-guarded fields to remain unchanged")
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func containsSubstring(items []string, needle string) bool {
	for _, item := range items {
		if strings.Contains(item, needle) {
			return true
		}
	}
	return false
}
