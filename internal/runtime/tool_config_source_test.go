// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

func TestPrepareToolConfigSourceSkipsWithoutPassthrough(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		profile: model.Profile{Name: "claude", ConfigDir: ".claude"},
	}

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}
	if r.configSourceDir != "" {
		t.Fatalf("expected empty configSourceDir, got %q", r.configSourceDir)
	}
}

func TestPrepareToolConfigSourceSkipsForBuiltInSettingsOnly(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := newTemplateOverrideRuntime(home, model.Profile{
		Name:           "pi",
		ConfigDir:      ".pi",
		SettingsFile:   "pi-settings.json",
		SettingsTarget: ".pi/agent/settings.json",
	})
	writeBuiltInToolTemplate(t, r, `{"source":"built-in"}`)

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}
	if r.configSourceDir != "" {
		t.Fatalf("expected empty configSourceDir for built-in settings-only tool, got %q", r.configSourceDir)
	}
}

func TestPrepareToolConfigSourceBuildsSettingsWhenConfigBaseExists(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := newTemplateOverrideRuntime(home, model.Profile{
		Name:           "pi",
		ConfigDir:      ".pi",
		SettingsFile:   "pi-settings.json",
		SettingsTarget: ".pi/agent/settings.json",
	})
	writeBuiltInToolTemplate(t, r, `{"source":"built-in"}`)
	configBase := filepath.Join(r.paths.ToolsDir, "pi", "config-base")
	if err := os.MkdirAll(configBase, 0o755); err != nil {
		t.Fatalf("mkdir config-base: %v", err)
	}

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}
	if r.configSourceDir == "" {
		t.Fatalf("expected configSourceDir to be set")
	}

	settingsBytes, err := os.ReadFile(filepath.Join(r.configSourceDir, "agent", "settings.json"))
	if err != nil {
		t.Fatalf("read generated settings: %v", err)
	}
	if string(settingsBytes) != `{"source":"built-in"}` {
		t.Fatalf("unexpected settings content: %s", string(settingsBytes))
	}
}

func TestPrepareToolConfigSourceComposesLayers(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	projectHash := "projhash"
	hostConfigDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(filepath.Join(hostConfigDir, "commands"), 0o755); err != nil {
		t.Fatalf("mkdir host commands: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(hostConfigDir, "todos"), 0o755); err != nil {
		t.Fatalf("mkdir host todos: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostConfigDir, "settings.json"), []byte(`{"source":"host"}`), 0o644); err != nil {
		t.Fatalf("write host settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostConfigDir, "commands", "review.md"), []byte("review"), 0o644); err != nil {
		t.Fatalf("write host command: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostConfigDir, "config.json"), []byte(`{"token":"secret"}`), 0o644); err != nil {
		t.Fatalf("write sensitive host file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostConfigDir, "todos", "current.json"), []byte(`{"todo":"secret"}`), 0o644); err != nil {
		t.Fatalf("write sensitive host dir file: %v", err)
	}

	globalConfigDir := config.HostToolConfigDir(home, "claude")
	projectConfigDir := config.HostProjectConfigDir(home, projectHash, "claude")
	if err := os.MkdirAll(filepath.Join(globalConfigDir, "commands"), 0o755); err != nil {
		t.Fatalf("mkdir global commands: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectConfigDir, "agents"), 0o755); err != nil {
		t.Fatalf("mkdir project agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(globalConfigDir, "commands", "global.md"), []byte("global-override"), 0o644); err != nil {
		t.Fatalf("write global override command: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectConfigDir, "settings.json"), []byte(`{"source":"project-override"}`), 0o644); err != nil {
		t.Fatalf("write project override settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectConfigDir, "agents", "project.md"), []byte("project-agent"), 0o644); err != nil {
		t.Fatalf("write project override agent: %v", err)
	}

	globalSkillsDir := config.HostSkillsDir(home)
	projectSkillsDir := config.HostProjectSkillsDir(home, projectHash)
	writeSkill(t, filepath.Join(globalSkillsDir, "global-only"), "global")
	writeSkill(t, filepath.Join(globalSkillsDir, "shared"), "global-shared")
	writeSkill(t, filepath.Join(projectSkillsDir, "project-only"), "project")
	writeSkill(t, filepath.Join(projectSkillsDir, "shared"), "project-shared")

	r := newTemplateOverrideRuntime(home, model.Profile{
		Name:           "claude",
		ConfigDir:      ".claude",
		SkillsDir:      ".claude/skills",
		SettingsFile:   "claude-settings.json",
		SettingsTarget: ".claude/settings.json",
		HostConfigDir:  ".claude",
		PassthroughPaths: []string{
			"commands/",
			"settings.json",
		},
		Providers: []model.ProviderConfig{
			{Name: "anthropic", AuthFiles: []string{"config.json"}},
		},
	})
	r.run = model.RunOptions{HostConfig: model.HostConfigPassthrough}
	writeBuiltInToolTemplate(t, r, `{"source":"built-in"}`)

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}

	if r.configSourceDir == "" {
		t.Fatal("expected configSourceDir to be set")
	}

	settingsBytes, err := os.ReadFile(filepath.Join(r.configSourceDir, "settings.json"))
	if err != nil {
		t.Fatalf("read generated settings: %v", err)
	}
	if string(settingsBytes) != `{"source":"project-override"}` {
		t.Fatalf("unexpected settings content: %s", string(settingsBytes))
	}
	commandBytes, err := os.ReadFile(filepath.Join(r.configSourceDir, "commands", "review.md"))
	if err != nil {
		t.Fatalf("read generated command: %v", err)
	}
	if string(commandBytes) != "review" {
		t.Fatalf("unexpected command content: %s", string(commandBytes))
	}
	globalCommandBytes, err := os.ReadFile(filepath.Join(r.configSourceDir, "commands", "global.md"))
	if err != nil {
		t.Fatalf("read global override command: %v", err)
	}
	if string(globalCommandBytes) != "global-override" {
		t.Fatalf("unexpected global override command content: %s", string(globalCommandBytes))
	}
	projectAgentBytes, err := os.ReadFile(filepath.Join(r.configSourceDir, "agents", "project.md"))
	if err != nil {
		t.Fatalf("read project override agent: %v", err)
	}
	if string(projectAgentBytes) != "project-agent" {
		t.Fatalf("unexpected project override agent content: %s", string(projectAgentBytes))
	}
	if _, err := os.Stat(filepath.Join(r.configSourceDir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("expected sensitive config.json to be filtered, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(r.configSourceDir, "todos")); !os.IsNotExist(err) {
		t.Fatalf("expected sensitive todos/ to be filtered, err=%v", err)
	}
	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "global-only"), "global")
	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "project-only"), "project")
	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "shared"), "project-shared")
}

func TestPrepareToolConfigSourceMergesBuiltinUserExtGlobalAndProjectSkills(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	projectHash := "projhash"
	userToolsDir := t.TempDir()

	r := newTemplateOverrideRuntime(home, model.Profile{
		Name:      "claude",
		ConfigDir: ".claude",
		SkillsDir: ".claude/skills",
	})
	r.paths.UserToolsDir = userToolsDir
	r.run = model.RunOptions{HostConfig: model.HostConfigPassthrough}

	// Built-in skills (lowest priority)
	builtinSkillsDir := filepath.Join(r.paths.ToolsDir, "claude", "skills")
	writeSkill(t, filepath.Join(builtinSkillsDir, "builtin-only"), "builtin")
	writeSkill(t, filepath.Join(builtinSkillsDir, "shared-bu"), "builtin-shared")
	writeSkill(t, filepath.Join(builtinSkillsDir, "shared-all"), "builtin-all")

	// User-extension skills (override built-in)
	userExtSkillsDir := filepath.Join(userToolsDir, "claude", "skills")
	writeSkill(t, filepath.Join(userExtSkillsDir, "user-ext-only"), "user-ext")
	writeSkill(t, filepath.Join(userExtSkillsDir, "shared-bu"), "user-ext-shared")
	writeSkill(t, filepath.Join(userExtSkillsDir, "shared-all"), "user-ext-all")

	globalSkillsDir := config.HostSkillsDir(home)
	writeSkill(t, filepath.Join(globalSkillsDir, "global-shared-only"), "global-shared")
	writeSkill(t, filepath.Join(globalSkillsDir, "shared-all"), "global-shared-all")
	writeSkill(t, filepath.Join(globalSkillsDir, "scope-order"), "global-shared")
	invalidSharedSkill := filepath.Join(globalSkillsDir, "harness-specific", "SKILL.md")
	writeRuntimeTestFile(t, invalidSharedSkill, "---\nname: harness-specific\ndescription: Harness-specific skill\nallowed-tools: Read\n---\ninvalid-shared")

	globalToolSkillsDir := filepath.Join(config.HostToolConfigDir(home, "claude"), "skills")
	writeSkill(t, filepath.Join(globalToolSkillsDir, "global-tool-only"), "global-tool")
	writeSkill(t, filepath.Join(globalToolSkillsDir, "shared-all"), "global-tool-all")
	writeSkill(t, filepath.Join(globalToolSkillsDir, "scope-order"), "global-tool")
	toolSpecificSkill := filepath.Join(globalToolSkillsDir, "harness-specific", "SKILL.md")
	writeRuntimeTestFile(t, toolSpecificSkill, "---\nname: harness-specific\ndescription: Harness-specific skill\nallowed-tools: Read\n---\ntool-specific")

	projectSkillsDir := config.HostProjectSkillsDir(home, projectHash)
	writeSkill(t, filepath.Join(projectSkillsDir, "project-shared-only"), "project-shared")
	writeSkill(t, filepath.Join(projectSkillsDir, "shared-all"), "project-shared-all")
	writeSkill(t, filepath.Join(projectSkillsDir, "scope-order"), "project-shared")

	projectToolSkillsDir := filepath.Join(config.HostProjectConfigDir(home, projectHash, "claude"), "skills")
	writeSkill(t, filepath.Join(projectToolSkillsDir, "project-tool-only"), "project-tool")
	writeSkill(t, filepath.Join(projectToolSkillsDir, "shared-all"), "project-tool-all")

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}

	if r.configSourceDir == "" {
		t.Fatal("expected configSourceDir to be set")
	}

	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "builtin-only"), "builtin")
	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "user-ext-only"), "user-ext")
	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "global-shared-only"), "global-shared")
	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "global-tool-only"), "global-tool")
	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "harness-specific"), "tool-specific")
	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "project-shared-only"), "project-shared")
	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "project-tool-only"), "project-tool")
	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "shared-bu"), "user-ext-shared")
	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "scope-order"), "project-shared")
	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "shared-all"), "project-tool-all")
}

func TestPrepareToolConfigSourceIncludesEnabledFeatureSkillsOnly(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	featuresDir := t.TempDir()

	r := newTemplateOverrideRuntime(home, model.Profile{
		Name:      "claude",
		ConfigDir: ".claude",
		SkillsDir: ".claude/skills",
	})
	r.paths.FeaturesDir = featuresDir
	r.features = []model.Extension{{Name: "example"}}
	r.run = model.RunOptions{HostConfig: model.HostConfigPassthrough}

	// An enabled feature ships skills; a present-but-not-enabled feature must
	// not contribute (off-by-default gating).
	writeSkill(t, filepath.Join(featuresDir, "example", "skills", "example-skill"), "feature-skill")
	writeSkill(t, filepath.Join(featuresDir, "other-feature", "skills", "other-skill"), "should-not-appear")
	// A feature skill name also present as a global shared skill: shared wins.
	writeSkill(t, filepath.Join(featuresDir, "example", "skills", "shared"), "feature-shared")
	writeSkill(t, filepath.Join(config.HostSkillsDir(home), "shared"), "global-shared")

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}

	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "example-skill"), "feature-skill")
	assertSkillBody(t, filepath.Join(r.configSourceDir, "skills", "shared"), "global-shared")
	if _, err := os.Stat(filepath.Join(r.configSourceDir, "skills", "other-skill")); !os.IsNotExist(err) {
		t.Fatalf("skill from a non-enabled feature must be absent, err=%v", err)
	}
}

func TestPrepareToolConfigSourceReplacesWholeSkillsAtEachLayer(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	projectHash := "projhash"
	r := newTemplateOverrideRuntime(home, model.Profile{
		Name:      "claude",
		ConfigDir: ".claude",
		SkillsDir: ".claude/skills",
	})

	builtInSkill := filepath.Join(r.paths.ToolsDir, "claude", "skills", "replace-me")
	writeSkill(t, builtInSkill, "built-in")
	if err := os.WriteFile(filepath.Join(builtInSkill, "built-in.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write built-in skill resource: %v", err)
	}

	globalSharedSkill := filepath.Join(config.HostSkillsDir(home), "replace-me")
	writeSkill(t, globalSharedSkill, "global-shared")
	if err := os.WriteFile(filepath.Join(globalSharedSkill, "global-shared.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write global shared skill resource: %v", err)
	}

	globalToolSkill := filepath.Join(config.HostToolConfigDir(home, "claude"), "skills", "replace-me")
	writeSkill(t, globalToolSkill, "global-tool")

	projectSharedSkill := filepath.Join(config.HostProjectSkillsDir(home, projectHash), "replace-me")
	writeSkill(t, projectSharedSkill, "project-shared")
	if err := os.WriteFile(filepath.Join(projectSharedSkill, "project-shared.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write project shared skill resource: %v", err)
	}

	projectToolSkill := filepath.Join(config.HostProjectConfigDir(home, projectHash, "claude"), "skills", "replace-me")
	writeSkill(t, projectToolSkill, "project-tool")

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}

	effectiveSkill := filepath.Join(r.configSourceDir, "skills", "replace-me")
	assertSkillBody(t, effectiveSkill, "project-tool")
	entries, err := os.ReadDir(effectiveSkill)
	if err != nil {
		t.Fatalf("read effective skill: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "SKILL.md" {
		t.Fatalf("expected whole-skill replacement, got entries %v", entries)
	}
}

func TestPrepareToolConfigSourceIgnoresSharedSkillsForUnsupportedTool(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeSkill(t, filepath.Join(config.HostSkillsDir(home), "portable"), "portable")
	r := newTemplateOverrideRuntime(home, model.Profile{Name: "theia", ConfigDir: ".theia"})

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}
	if r.configSourceDir != "" {
		t.Fatalf("expected shared skills to leave unsupported tool unchanged, got %q", r.configSourceDir)
	}
}

func TestResolveHostConfigPathsFiltersSensitiveBackstop(t *testing.T) {
	t.Parallel()

	profile := model.Profile{
		PassthroughPaths: []string{
			"commands/",
			"history.jsonl",
			".claude.json",
		},
		Providers: []model.ProviderConfig{
			{Name: "anthropic", AuthFiles: []string{"config.json"}},
		},
		HostOAuthJSON: ".claude.json",
	}

	got := config.ResolveHostConfigPaths(profile, []string{
		"default",
		"+config.json",
		"+todos/",
	})
	want := []string{"commands/"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("unexpected resolved host config paths: got %v want %v", got, want)
	}
}

func TestApplyHostConfigLayerBlocksSensitivePathsEvenWhenRequested(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	hostConfigDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(filepath.Join(hostConfigDir, "commands"), 0o755); err != nil {
		t.Fatalf("mkdir host commands: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(hostConfigDir, "todos"), 0o755); err != nil {
		t.Fatalf("mkdir host todos: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostConfigDir, "settings.json"), []byte(`{"source":"host"}`), 0o644); err != nil {
		t.Fatalf("write host settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostConfigDir, "commands", "review.md"), []byte("review"), 0o644); err != nil {
		t.Fatalf("write host command: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostConfigDir, "history.jsonl"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write host history: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauth":"secret"}`), 0o644); err != nil {
		t.Fatalf("write host oauth json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostConfigDir, "config.json"), []byte(`{"token":"secret"}`), 0o644); err != nil {
		t.Fatalf("write host auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostConfigDir, "todos", "current.json"), []byte(`{"todo":"secret"}`), 0o644); err != nil {
		t.Fatalf("write host todo: %v", err)
	}

	r := newTemplateOverrideRuntime(home, model.Profile{
		Name:           "claude",
		ConfigDir:      ".claude",
		HostConfigDir:  ".claude",
		SettingsFile:   "claude-settings.json",
		SettingsTarget: ".claude/settings.json",
		PassthroughPaths: []string{
			"commands/",
			"settings.json",
			"history.jsonl",
			"config.json",
			"todos/",
		},
		Providers: []model.ProviderConfig{
			{Name: "anthropic", AuthFiles: []string{"config.json"}},
		},
		HostOAuthJSON: ".claude.json",
	})
	r.run = model.RunOptions{
		HostConfig: model.HostConfigPassthrough,
		HostConfigPaths: []string{
			"default",
			"+.claude.json",
		},
	}
	writeBuiltInToolTemplate(t, r, `{"source":"built-in"}`)

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}

	assertPathExists(t, filepath.Join(r.configSourceDir, "settings.json"))
	assertPathExists(t, filepath.Join(r.configSourceDir, "commands", "review.md"))
	assertPathMissing(t, filepath.Join(r.configSourceDir, "history.jsonl"))
	assertPathMissing(t, filepath.Join(r.configSourceDir, "config.json"))
	assertPathMissing(t, filepath.Join(r.configSourceDir, "todos"))
	assertPathMissing(t, filepath.Join(r.configSourceDir, ".claude.json"))
}

func TestApplyHostConfigLayerBlocksNestedSensitivePathsInsideAllowedDirectory(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	hostConfigDir := filepath.Join(home, ".pi")
	if err := os.MkdirAll(filepath.Join(hostConfigDir, "agent"), 0o755); err != nil {
		t.Fatalf("mkdir host agent dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostConfigDir, "agent", "settings.json"), []byte(`{"source":"host"}`), 0o644); err != nil {
		t.Fatalf("write host settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostConfigDir, "agent", "auth.json"), []byte(`{"token":"secret"}`), 0o644); err != nil {
		t.Fatalf("write host auth: %v", err)
	}

	r := newTemplateOverrideRuntime(home, model.Profile{
		Name:           "pi",
		ConfigDir:      ".pi",
		HostConfigDir:  ".pi",
		SettingsFile:   "pi-settings.json",
		SettingsTarget: ".pi/agent/settings.json",
		PassthroughPaths: []string{
			"agent/settings.json",
		},
		Providers: []model.ProviderConfig{
			{Name: "openai-codex", AuthFiles: []string{"agent/auth.json"}},
		},
	})
	r.run = model.RunOptions{
		HostConfig: model.HostConfigPassthrough,
		HostConfigPaths: []string{
			"agent/",
		},
	}
	writeBuiltInToolTemplate(t, r, `{"source":"built-in"}`)

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}

	assertPathExists(t, filepath.Join(r.configSourceDir, "agent", "settings.json"))
	assertPathMissing(t, filepath.Join(r.configSourceDir, "agent", "auth.json"))
}

func TestApplyHostConfigLayerFollowsSymlinkedHostConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	// Real files that home-manager/Nix-style symlinks point at, living outside
	// the host config dir (mirrors ~/.claude/* -> /nix/store/...).
	storeDir := filepath.Join(home, "store")
	if err := os.MkdirAll(filepath.Join(storeDir, "commands"), 0o755); err != nil {
		t.Fatalf("mkdir store commands: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "settings.json"), []byte(`{"source":"host-symlink"}`), 0o644); err != nil {
		t.Fatalf("write store settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "statusline-command.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatalf("write store statusline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "commands", "review.md"), []byte("review"), 0o644); err != nil {
		t.Fatalf("write store command: %v", err)
	}

	// The host config dir holds only symlinks, exactly as home-manager lays out
	// ~/.claude: a symlinked file, plus a symlinked directory.
	hostConfigDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(hostConfigDir, 0o755); err != nil {
		t.Fatalf("mkdir host config: %v", err)
	}
	mustSymlink(t, filepath.Join(storeDir, "settings.json"), filepath.Join(hostConfigDir, "settings.json"))
	mustSymlink(t, filepath.Join(storeDir, "statusline-command.sh"), filepath.Join(hostConfigDir, "statusline-command.sh"))
	mustSymlink(t, filepath.Join(storeDir, "commands"), filepath.Join(hostConfigDir, "commands"))

	r := newTemplateOverrideRuntime(home, model.Profile{
		Name:           "claude",
		ConfigDir:      ".claude",
		HostConfigDir:  ".claude",
		SettingsFile:   "claude-settings.json",
		SettingsTarget: ".claude/settings.json",
		PassthroughPaths: []string{
			"commands/",
			"settings.json",
		},
		Providers: []model.ProviderConfig{
			{Name: "anthropic", AuthFiles: []string{"config.json"}},
		},
	})
	r.run = model.RunOptions{
		HostConfig: model.HostConfigPassthrough,
		HostConfigPaths: []string{
			"default",
			"+statusline-command.sh",
		},
	}
	writeBuiltInToolTemplate(t, r, `{"source":"built-in"}`)

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}
	if r.configSourceDir == "" {
		t.Fatal("expected configSourceDir to be set")
	}

	// Symlinked file: the host settings must override the built-in template.
	settingsBytes, err := os.ReadFile(filepath.Join(r.configSourceDir, "settings.json"))
	if err != nil {
		t.Fatalf("read generated settings: %v", err)
	}
	if string(settingsBytes) != `{"source":"host-symlink"}` {
		t.Fatalf("expected symlinked host settings to pass through, got: %s", settingsBytes)
	}

	// Symlinked file added via host_config_paths must pass through too.
	statusline, err := os.ReadFile(filepath.Join(r.configSourceDir, "statusline-command.sh"))
	if err != nil {
		t.Fatalf("read generated statusline: %v", err)
	}
	if string(statusline) != "#!/bin/sh\necho hi\n" {
		t.Fatalf("unexpected statusline content: %s", statusline)
	}

	// Symlinked directory: files inside the resolved target must pass through.
	commandBytes, err := os.ReadFile(filepath.Join(r.configSourceDir, "commands", "review.md"))
	if err != nil {
		t.Fatalf("read generated command from symlinked dir: %v", err)
	}
	if string(commandBytes) != "review" {
		t.Fatalf("unexpected command content: %s", commandBytes)
	}

	// The generated tree must hold regular files, not the symlinks — the Nix
	// store is not mounted in the container.
	for _, rel := range []string{"settings.json", "statusline-command.sh", "commands/review.md"} {
		info, err := os.Lstat(filepath.Join(r.configSourceDir, rel))
		if err != nil {
			t.Fatalf("lstat generated %s: %v", rel, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("expected generated %s to be a regular file, got a symlink", rel)
		}
	}
}

func TestApplyHostConfigLayerBlocksSymlinkAliasingDeniedPath(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	hostConfigDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(hostConfigDir, 0o755); err != nil {
		t.Fatalf("mkdir host config: %v", err)
	}
	// A denied auth file, plus an allowed name symlinked straight at it.
	if err := os.WriteFile(filepath.Join(hostConfigDir, "config.json"), []byte(`{"token":"secret"}`), 0o600); err != nil {
		t.Fatalf("write denied auth file: %v", err)
	}
	mustSymlink(t, filepath.Join(hostConfigDir, "config.json"), filepath.Join(hostConfigDir, "settings.json"))

	r := newTemplateOverrideRuntime(home, model.Profile{
		Name:           "claude",
		ConfigDir:      ".claude",
		HostConfigDir:  ".claude",
		SettingsFile:   "claude-settings.json",
		SettingsTarget: ".claude/settings.json",
		PassthroughPaths: []string{
			"settings.json",
		},
		Providers: []model.ProviderConfig{
			{Name: "anthropic", AuthFiles: []string{"config.json"}},
		},
	})
	r.run = model.RunOptions{HostConfig: model.HostConfigPassthrough}
	writeBuiltInToolTemplate(t, r, `{"source":"built-in"}`)

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}

	// The denied auth file must not pass through, neither directly nor aliased.
	assertPathMissing(t, filepath.Join(r.configSourceDir, "config.json"))
	settingsBytes, err := os.ReadFile(filepath.Join(r.configSourceDir, "settings.json"))
	if err != nil {
		t.Fatalf("read generated settings: %v", err)
	}
	if strings.Contains(string(settingsBytes), "secret") {
		t.Fatalf("denied auth content leaked through aliased symlink: %s", settingsBytes)
	}
	// With the alias skipped, the built-in template settings remain in place.
	if string(settingsBytes) != `{"source":"built-in"}` {
		t.Fatalf("expected built-in settings to remain, got: %s", settingsBytes)
	}
}

func TestApplyHostConfigLayerBlocksSymlinkAliasingExternalOAuthFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	hostConfigDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(hostConfigDir, 0o755); err != nil {
		t.Fatalf("mkdir host config: %v", err)
	}
	// The OAuth file lives in $HOME, outside the config dir.
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"claudeAiOauth":{"accessToken":"secret"}}`), 0o600); err != nil {
		t.Fatalf("write host oauth json: %v", err)
	}
	// An allowed config name symlinked at that external OAuth file.
	mustSymlink(t, filepath.Join(home, ".claude.json"), filepath.Join(hostConfigDir, "settings.json"))

	r := newTemplateOverrideRuntime(home, model.Profile{
		Name:           "claude",
		ConfigDir:      ".claude",
		HostConfigDir:  ".claude",
		SettingsFile:   "claude-settings.json",
		SettingsTarget: ".claude/settings.json",
		PassthroughPaths: []string{
			"settings.json",
		},
		Providers: []model.ProviderConfig{
			{Name: "anthropic", AuthFiles: []string{"config.json"}},
		},
		HostOAuthJSON: ".claude.json",
	})
	r.run = model.RunOptions{HostConfig: model.HostConfigPassthrough}
	writeBuiltInToolTemplate(t, r, `{"source":"built-in"}`)

	if err := r.prepareToolConfigSource(); err != nil {
		t.Fatalf("prepareToolConfigSource returned error: %v", err)
	}

	settingsBytes, err := os.ReadFile(filepath.Join(r.configSourceDir, "settings.json"))
	if err != nil {
		t.Fatalf("read generated settings: %v", err)
	}
	if strings.Contains(string(settingsBytes), "secret") {
		t.Fatalf("external OAuth content leaked through aliased symlink: %s", settingsBytes)
	}
	// With the alias skipped, the built-in template settings remain in place.
	if string(settingsBytes) != `{"source":"built-in"}` {
		t.Fatalf("expected built-in settings to remain, got: %s", settingsBytes)
	}
}

func mustSymlink(t *testing.T, oldname string, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Fatalf("symlink %s -> %s: %v", newname, oldname, err)
	}
}

func assertPathExists(t *testing.T, target string) {
	t.Helper()
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected %s to exist, err=%v", target, err)
	}
}

func assertPathMissing(t *testing.T, target string) {
	t.Helper()
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be absent, err=%v", target, err)
	}
}

func TestConfigSourcePreservePathsIncludesRuntimeStateAndAuth(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		profile: model.Profile{
			Name: "pi",
			Providers: []model.ProviderConfig{
				{Name: "openai-codex", AuthFiles: []string{"agent/auth.json"}},
			},
		},
	}

	got := r.configSourcePreservePaths()
	joined := "," + strings.Join(got, ",") + ","
	for _, want := range []string{".enclave-devcontainer/", "agent/auth.json", "sessions/", "agent/sessions/", "session/", "projects/", "history.jsonl"} {
		if !strings.Contains(joined, ","+want+",") {
			t.Fatalf("configSourcePreservePaths() missing %q in %v", want, got)
		}
	}
}
