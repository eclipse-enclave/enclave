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

func TestSkillsValidationConfigPrecedence(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"skills_validation":"agent","tool_overrides":{"codex":{"skills_validation":"strict"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	global, warnings, err := readDefaults(configPath)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("read defaults: %v, %v", err, warnings)
	}
	project := Defaults{SkillsValidation: model.SkillsValidationStrict}
	warnings = stripGlobalOnlyKeys("/tmp/projects/abc/config.json", &project)
	if len(warnings) != 0 || project.SkillsValidation != model.SkillsValidationStrict {
		t.Fatalf("project validation option was filtered: %+v, %v", project, warnings)
	}
	for _, tc := range []struct {
		name    string
		global  Defaults
		project Defaults
		tool    string
		want    string
		source  model.OptionSource
	}{
		{"default", Defaults{}, Defaults{}, "claude", model.SkillsValidationStrict, model.SourceDefault},
		{"global", global, Defaults{}, "claude", model.SkillsValidationAgent, model.SourceGlobal},
		{"project", global, project, "claude", model.SkillsValidationStrict, model.SourceProject},
		{"tool", global, Defaults{SkillsValidation: model.SkillsValidationAgent}, "codex", model.SkillsValidationStrict, model.SourceToolOverride},
		{"project tool", global, Defaults{ToolOverrides: map[string]Defaults{"codex": {SkillsValidation: model.SkillsValidationAgent}}}, "codex", model.SkillsValidationAgent, model.SourceToolOverride},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _, _ := ResolveOptionsForTool(DefaultOptions(), model.DefaultOptionSources(), tc.global, tc.project, tc.tool)
			if opts.SkillsValidation != tc.want || opts.Sources.SkillsValidation != tc.source {
				t.Fatalf("validation mode/source = %q/%v, want %q/%v", opts.SkillsValidation, opts.Sources.SkillsValidation, tc.want, tc.source)
			}
		})
	}
}

func TestAutoUpdateIsGlobalOnly(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(globalPath, []byte(`{"auto_update":true,"tool_overrides":{"codex":{"auto_update":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	global, warnings, err := readDefaults(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if global.AutoUpdate == nil || !*global.AutoUpdate {
		t.Fatalf("global auto_update was not retained: %+v", global)
	}
	if global.ToolOverrides["codex"].AutoUpdate != nil {
		t.Fatal("tool override auto_update was retained")
	}
	if !containsSubstring(warnings, "configured globally") {
		t.Fatalf("missing tool override warning: %v", warnings)
	}

	project := Defaults{AutoUpdate: boolPtr(false)}
	warnings = stripGlobalOnlyKeys(globalPath, &project)
	if project.AutoUpdate != nil {
		t.Fatal("project auto_update was retained")
	}
	if !containsSubstring(warnings, "configured globally") {
		t.Fatalf("missing project warning: %v", warnings)
	}
}

func TestUpdateChannelIsGlobalOnlyAndValidated(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(globalPath, []byte(`{"update_channel":" stable ","tool_overrides":{"codex":{"update_channel":"rolling"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	global, warnings, err := readDefaults(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if global.UpdateChannel != "stable" {
		t.Fatalf("global update_channel = %q, want stable", global.UpdateChannel)
	}
	if global.ToolOverrides["codex"].UpdateChannel != "" || !containsSubstring(warnings, "tool_overrides.codex.update_channel") {
		t.Fatalf("tool override update_channel was retained or not warned about: %v", warnings)
	}

	if err := os.WriteFile(globalPath, []byte(`{"update_channel":"nightly"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	global, warnings, err = readDefaults(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if global.UpdateChannel != "" || !containsSubstring(warnings, `update_channel "nightly"`) {
		t.Fatalf("unknown channel %q retained or not warned about: %v", global.UpdateChannel, warnings)
	}

	project := Defaults{UpdateChannel: "rolling"}
	warnings = stripGlobalOnlyKeys(globalPath, &project)
	if project.UpdateChannel != "" || !containsSubstring(warnings, "configured globally") {
		t.Fatalf("project update_channel retained or not warned about: %v", warnings)
	}
}

func TestToolOverride_SingleToolApplied(t *testing.T) {
	sources := model.DefaultOptionSources()
	opts := DefaultOptions()

	project := Defaults{
		ToolOverrides: map[string]Defaults{
			"pi": {NoAPIKey: boolPtr(true)},
		},
	}

	opts, _, ok := ResolveOptionsForTool(opts, sources, Defaults{}, project, "pi")
	if !ok {
		t.Fatalf("expected tool override for pi")
	}
	if !opts.NoAPIKey {
		t.Fatalf("expected no_api_key to be set by tool override")
	}
	if opts.Sources.NoAPIKey != model.SourceToolOverride {
		t.Fatalf("expected no_api_key source=%v, got %v", model.SourceToolOverride, opts.Sources.NoAPIKey)
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

	opts, _, ok := ResolveOptionsForTool(opts, sources, Defaults{}, project, "pi")
	if !ok {
		t.Fatalf("expected tool override for pi")
	}
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

// sensitive_dir_exceptions is a list like add_dirs: a global tool override
// replaces the top-level list instead of extending it.
func TestToolOverride_GlobalSensitiveDirExceptionsReplaceTopLevel(t *testing.T) {
	global := Defaults{
		SensitiveDirExceptions: []string{"~/.m2"},
		ToolOverrides: map[string]Defaults{
			"claude": {SensitiveDirExceptions: []string{"~/.gradle"}},
		},
	}
	for _, tc := range []struct {
		tool   string
		want   []string
		source model.OptionSource
	}{
		{tool: "claude", want: []string{"~/.gradle"}, source: model.SourceToolOverride},
		{tool: "codex", want: []string{"~/.m2"}, source: model.SourceGlobal},
	} {
		opts, _, _ := ResolveOptionsForTool(DefaultOptions(), model.OptionSources{}, global, Defaults{}, tc.tool)
		if !reflect.DeepEqual(opts.SensitiveDirExceptions, tc.want) || opts.Sources.SensitiveDirExceptions != tc.source {
			t.Errorf("tool %s: sensitive_dir_exceptions = %q from %v, want %q from %v",
				tc.tool, opts.SensitiveDirExceptions, opts.Sources.SensitiveDirExceptions, tc.want, tc.source)
		}
	}
}

func TestToolOverride_CLIWinsOverToolOverride(t *testing.T) {
	sources := model.DefaultOptionSources()
	opts := DefaultOptions()
	opts.NoAPIKey = true
	sources.NoAPIKey = model.SourceCLI

	global := Defaults{ToolOverrides: map[string]Defaults{"pi": {NoAPIKey: boolPtr(false)}}}
	opts, _, _ = ResolveOptionsForTool(opts, sources, global, Defaults{}, "pi")

	if !opts.NoAPIKey {
		t.Fatalf("tool override should not override CLI value")
	}
	if opts.Sources.NoAPIKey != model.SourceCLI {
		t.Fatalf("expected CLI source to remain unchanged, got %v", opts.Sources.NoAPIKey)
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

func TestStripGlobalOnlyKeysKeepsEverythingElse(t *testing.T) {
	settable := Defaults{
		Tool:             "codex",
		Yolo:             boolPtr(true),
		AllowAllNetwork:  boolPtr(true),
		AllowDomains:     []string{"proj.example.com"},
		PassEnv:          []string{"GH_TOKEN"},
		BridgePorts:      []string{"5432"},
		HostConfig:       "passthrough",
		HostConfigPaths:  []string{"+commands/"},
		BaseImage:        "debian:trixie",
		AddDirs:          []string{"/data/shared"},
		AddReadonlyDirs:  []string{"/etc"},
		ProjectMount:     model.ProjectMountWritable,
		WorktreeMetadata: model.WorktreeMetadataFollow,
	}
	defaults := settable
	defaults.AutoUpdate = boolPtr(true)
	defaults.UpdateChannel = "rolling"
	override := settable
	override.Tool = ""
	defaults.ToolOverrides = map[string]Defaults{"codex": override}

	warnings := stripGlobalOnlyKeys("/tmp/projects/abc/config.json", &defaults)
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want auto_update and update_channel", warnings)
	}
	for _, w := range warnings {
		if w.Code != WarningProjectGuardrail || !strings.Contains(w.Message, "configured globally") {
			t.Fatalf("unexpected warning %+v", w)
		}
	}
	want := settable
	want.ToolOverrides = map[string]Defaults{"codex": override}
	if !reflect.DeepEqual(defaults, want) {
		t.Fatalf("defaults = %+v, want only the update keys removed", defaults)
	}
}

func TestIsPathWithinProjectDir(t *testing.T) {
	projectDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outsideDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(projectDir, "escape")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		entry string
		want  bool
	}{
		{entry: "./data", want: true},
		{entry: filepath.Join(projectDir, "data"), want: true},
		{entry: "", want: false},
		{entry: "/etc", want: false},
		{entry: "~/.aws", want: false},
		{entry: "../sibling/path", want: false},
		{entry: "./escape", want: false},
	}
	for _, tc := range cases {
		if got := IsPathWithinProjectDir(tc.entry, projectDir); got != tc.want {
			t.Errorf("IsPathWithinProjectDir(%q) = %v, want %v", tc.entry, got, tc.want)
		}
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

func TestReadDefaults_SessionTintDropsInvalidValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	payload := `{
  "session_tint": "red",
  "tool_overrides": {
    "claude": {
      "session_tint": "#2a0f12"
    },
    "codex": {
      "session_tint": "#2a0"
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
	if defaults.SessionTint != "" {
		t.Fatalf("expected invalid session_tint to be dropped, got %q", defaults.SessionTint)
	}
	if got := defaults.ToolOverrides["claude"].SessionTint; got != "#2a0f12" {
		t.Fatalf("expected valid claude session_tint to remain, got %q", got)
	}
	if got := defaults.ToolOverrides["codex"].SessionTint; got != "" {
		t.Fatalf("expected invalid codex session_tint to be dropped, got %q", got)
	}
	if !containsSubstring(warnings, "session_tint \"red\" in "+path) {
		t.Fatalf("expected warning naming the config file, warnings=%v", warnings)
	}
	if !containsSubstring(warnings, "tool_overrides.codex.session_tint \"#2a0\"") {
		t.Fatalf("expected tool override warning for codex, warnings=%v", warnings)
	}
	if containsSubstring(warnings, "claude") {
		t.Fatalf("did not expect a warning for the valid claude value: %v", warnings)
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

func boolPtr(v bool) *bool {
	return &v
}

func containsSubstring(items []Warning, needle string) bool {
	for _, item := range items {
		if strings.Contains(item.Message, needle) {
			return true
		}
	}
	return false
}
