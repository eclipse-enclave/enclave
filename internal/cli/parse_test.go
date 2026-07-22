// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package cli

import (
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/usercmd"
)

func TestParseRunArgsDelimiter(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--tool", "codex", "--", "--flag", "value"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "run" {
		t.Fatalf("expected action run, got %s", res.Action)
	}
	if res.Options.Tool != "codex" {
		t.Fatalf("expected tool codex, got %s", res.Options.Tool)
	}
	if res.Sources.Tool != model.SourceCLI {
		t.Fatalf("expected tool source CLI, got %v", res.Sources.Tool)
	}
	expected := []string{"--flag", "value"}
	if !reflect.DeepEqual(res.Options.CmdArgs, expected) {
		t.Fatalf("expected cmd args %v, got %v", expected, res.Options.CmdArgs)
	}
}

func TestParseContinueArgsDelimiter(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"continue", "--tool", "codex", "--", "--flag", "value"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "continue" {
		t.Fatalf("expected action continue, got %s", res.Action)
	}
	if res.Options.Tool != "codex" {
		t.Fatalf("expected tool codex, got %s", res.Options.Tool)
	}
	expected := []string{"--flag", "value"}
	if !reflect.DeepEqual(res.Options.CmdArgs, expected) {
		t.Fatalf("expected cmd args %v, got %v", expected, res.Options.CmdArgs)
	}
}

func TestParseResumeBackground(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"resume", "--background", "--", "--flag"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "resume" {
		t.Fatalf("expected action resume, got %s", res.Action)
	}
	if !res.Options.Background {
		t.Fatal("expected background true")
	}
	expected := []string{"--flag"}
	if !reflect.DeepEqual(res.Options.CmdArgs, expected) {
		t.Fatalf("expected cmd args %v, got %v", expected, res.Options.CmdArgs)
	}
}

func TestParseShellAdmin(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"shell", "--admin", "--", "ls"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "shell" {
		t.Fatalf("expected action shell, got %s", res.Action)
	}
	if !res.Options.Admin {
		t.Fatal("expected admin true")
	}
	if !res.Options.Shell {
		t.Fatal("expected shell true")
	}
	expected := []string{"ls"}
	if !reflect.DeepEqual(res.Options.CmdArgs, expected) {
		t.Fatalf("expected cmd args %v, got %v", expected, res.Options.CmdArgs)
	}
}

func TestParseCleanupFlags(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"cleanup", "--all", "--keep", "memory", "--tool", "codex"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "cleanup" {
		t.Fatalf("expected action cleanup, got %s", res.Action)
	}
	if !res.Options.CleanupAll {
		t.Fatal("expected cleanup all true")
	}
	if !res.Options.CleanupKeepMemory {
		t.Fatal("expected cleanup keep-memory true")
	}
	if res.Options.Tool != "codex" {
		t.Fatalf("expected tool codex, got %s", res.Options.Tool)
	}
}

func TestParseUpdateCommand(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"update", "codex", "claude"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "update" {
		t.Fatalf("expected action update, got %s", res.Action)
	}
	if got := res.Options.UpdateTools; len(got) != 2 || got[0] != "codex" || got[1] != "claude" {
		t.Fatalf("expected update tools [codex claude], got %v", got)
	}
}

func TestParseUpdateCommandBuildFlags(t *testing.T) {
	defaults := config.DefaultOptions()
	// update builds an image, so it must accept the same build-affecting flags
	// as run (e.g. --slim) and --tool for the no-argument default.
	res, err := Parse([]string{"update", "--slim", "--tool", "codex"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "update" {
		t.Fatalf("expected action update, got %s", res.Action)
	}
	if !res.Options.Slim {
		t.Fatal("expected --slim to set Slim on the update command")
	}
	if res.Options.Tool != "codex" {
		t.Fatalf("expected tool codex, got %s", res.Options.Tool)
	}
	if len(res.Options.UpdateTools) != 0 {
		t.Fatalf("expected no positional tools, got %v", res.Options.UpdateTools)
	}
}

func TestParsePS(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"ps", "--tool", "codex", "--name", "main"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "ps" {
		t.Fatalf("expected action ps, got %s", res.Action)
	}
	if res.Options.Tool != "codex" {
		t.Fatalf("expected tool codex, got %s", res.Options.Tool)
	}
	if res.Options.SessionName != "main" {
		t.Fatalf("expected session name main, got %s", res.Options.SessionName)
	}
	if res.Sources.Tool != model.SourceCLI {
		t.Fatalf("expected tool source CLI, got %v", res.Sources.Tool)
	}
	if res.Sources.SessionName != model.SourceCLI {
		t.Fatalf("expected session source CLI, got %v", res.Sources.SessionName)
	}
}

func TestParseConfigFlags(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{
		"config",
		"--view", "effective",
		"--json",
		"--ephemeral",
		"--yolo",
		"--reset-auth",
		"--slim",
		"--features", "node-dev",
	}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "config" {
		t.Fatalf("expected action config, got %s", res.Action)
	}
	if res.ConfigView.Mode != "effective" || !res.ConfigView.JSON {
		t.Fatalf("expected view=effective and json=true, got view=%q json=%v", res.ConfigView.Mode, res.ConfigView.JSON)
	}
	if !res.Options.Ephemeral || res.Sources.Ephemeral != model.SourceCLI {
		t.Fatalf("expected ephemeral CLI override, got value=%v source=%v", res.Options.Ephemeral, res.Sources.Ephemeral)
	}
	if res.Options.YoloOverride == nil || !*res.Options.YoloOverride || res.Sources.Yolo != model.SourceCLI {
		t.Fatalf("expected yolo CLI override, got value=%v source=%v", res.Options.YoloOverride, res.Sources.Yolo)
	}
	if !res.Options.ResetAuth || res.Sources.ResetAuth != model.SourceCLI {
		t.Fatalf("expected reset-auth CLI override, got value=%v source=%v", res.Options.ResetAuth, res.Sources.ResetAuth)
	}
	if !res.Options.Slim || res.Sources.Slim != model.SourceCLI {
		t.Fatalf("expected slim CLI override, got value=%v source=%v", res.Options.Slim, res.Sources.Slim)
	}
	expectedFeatures := []string{"node-dev"}
	if !reflect.DeepEqual(res.Options.Features, expectedFeatures) || res.Sources.Features != model.SourceCLI {
		t.Fatalf("expected features CLI override, got value=%v source=%v", res.Options.Features, res.Sources.Features)
	}
}

func TestParseToolValueDoesNotBecomeCommand(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--tool", "exec"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "run" {
		t.Fatalf("expected action run, got %s", res.Action)
	}
	if res.Options.Tool != "exec" {
		t.Fatalf("expected tool exec, got %s", res.Options.Tool)
	}
}

func TestParseUnknownFlagErrors(t *testing.T) {
	defaults := config.DefaultOptions()
	if _, err := Parse([]string{"--baseimage", "ubuntu:24.04"}, defaults); err == nil {
		t.Fatal("expected error for unknown flag")
	}
}

// The documented `[FLAGS] [COMMAND]` form must work for subcommand-specific
// value flags: their value must not be mistaken for the command name.
func TestParseSubcommandValueFlagBeforeCommand(t *testing.T) {
	defaults := config.DefaultOptions()

	res, err := Parse([]string{"--view", "effective", "config"}, defaults)
	if err != nil {
		t.Fatalf("parse failed for --view before config: %v", err)
	}
	if res.Action != "config" {
		t.Fatalf("expected action config, got %s", res.Action)
	}
	if res.ConfigView.Mode != "effective" {
		t.Fatalf("expected view effective, got %q", res.ConfigView.Mode)
	}

	// "auth" is also a real command; --keep must consume it as its value rather
	// than let it hijack the command chain.
	res, err = Parse([]string{"--keep", "auth", "cleanup"}, defaults)
	if err != nil {
		t.Fatalf("parse failed for --keep before cleanup: %v", err)
	}
	if res.Action != "cleanup" {
		t.Fatalf("expected action cleanup, got %s", res.Action)
	}
	if !res.Options.CleanupKeepAuth {
		t.Fatal("expected --keep auth to set CleanupKeepAuth")
	}
}

func TestParseConfigViewInvalid(t *testing.T) {
	defaults := config.DefaultOptions()
	_, err := Parse([]string{"config", "--view", "bogus"}, defaults)
	if err == nil {
		t.Fatal("expected error for invalid --view value")
	}
	if !strings.Contains(err.Error(), "invalid --view") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseCleanupKeepInvalid(t *testing.T) {
	defaults := config.DefaultOptions()
	_, err := Parse([]string{"cleanup", "--keep", "bogus"}, defaults)
	if err == nil {
		t.Fatal("expected error for invalid --keep value")
	}
	if !strings.Contains(err.Error(), "unknown --keep value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseBuildCacheAndIdentityFlags(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{
		"--build-uid", "1000",
		"--build-gid", "1000",
		"--runtime-uid-remap",
		"--buildx-cache-dir", "/tmp/enclave-cache",
		"--buildx-cache-from", "type=registry,ref=example.test/cache",
		"--buildx-cache-to", "type=registry,ref=example.test/cache,mode=max",
	}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Options.BuildUID != "1000" || res.Options.BuildGID != "1000" {
		t.Fatalf("unexpected build identity: uid=%q gid=%q", res.Options.BuildUID, res.Options.BuildGID)
	}
	if !res.Options.RuntimeUIDRemap {
		t.Fatal("expected runtime uid remap")
	}
	if res.Options.BuildxCacheDir != "/tmp/enclave-cache" {
		t.Fatalf("unexpected buildx cache dir: %q", res.Options.BuildxCacheDir)
	}
	if !reflect.DeepEqual(res.Options.BuildxCacheFrom, []string{"type=registry,ref=example.test/cache"}) {
		t.Fatalf("unexpected buildx cache-from: %v", res.Options.BuildxCacheFrom)
	}
	if !reflect.DeepEqual(res.Options.BuildxCacheTo, []string{"type=registry,ref=example.test/cache,mode=max"}) {
		t.Fatalf("unexpected buildx cache-to: %v", res.Options.BuildxCacheTo)
	}
}

func TestParseRejectsRemovedBuildFlags(t *testing.T) {
	defaults := config.DefaultOptions()
	for _, flag := range []string{"--image-mode", "--no-agents", "--agent-tools", "--update-agents", "--update-agent", "--build-backend"} {
		if _, err := Parse([]string{flag}, defaults); err == nil {
			t.Fatalf("expected error for removed flag %s", flag)
		}
	}
}

func TestParseUnknownSubcommandErrors(t *testing.T) {
	defaults := config.DefaultOptions()
	_, err := Parse([]string{"nonExistingCommand"}, defaults)
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown command") || !strings.Contains(msg, "nonExistingCommand") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseUnknownSubcommandAfterFlagErrors(t *testing.T) {
	defaults := config.DefaultOptions()
	_, err := Parse([]string{"--tool", "codex", "bogus"}, defaults)
	if err == nil {
		t.Fatal("expected error for unknown subcommand following flag")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseUnknownSubSubcommandErrors(t *testing.T) {
	defaults := config.DefaultOptions()
	cases := []struct {
		name string
		args []string
	}{
		{"extension", []string{"extension", "nope"}},
		{"auth", []string{"auth", "nope"}},
		{"network", []string{"network", "nope"}},
		{"devcontainer", []string{"devcontainer", "nope"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.args, defaults)
			if err == nil {
				t.Fatal("expected error for unknown sub-subcommand")
			}
			if !strings.Contains(err.Error(), "nope") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestParseDashDashPassesUnknownThrough(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--", "bogus"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "run" {
		t.Fatalf("expected action run, got %s", res.Action)
	}
	if !reflect.DeepEqual(res.Options.CmdArgs, []string{"bogus"}) {
		t.Fatalf("expected cmd args [bogus], got %v", res.Options.CmdArgs)
	}
}

func TestParseAuthImport(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"auth", "import", "--tool", "codex"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "auth-import" {
		t.Fatalf("expected action auth-import, got %s", res.Action)
	}
	if res.Options.Tool != "codex" {
		t.Fatalf("expected tool codex, got %s", res.Options.Tool)
	}
	if res.Sources.Tool != model.SourceCLI {
		t.Fatalf("expected tool source CLI, got %v", res.Sources.Tool)
	}
}

func TestParseAuthExport(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"auth", "export", "--tool", "claude"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "auth-export" {
		t.Fatalf("expected action auth-export, got %s", res.Action)
	}
	if res.Options.Tool != "claude" {
		t.Fatalf("expected tool claude, got %s", res.Options.Tool)
	}
}

func TestParseEphemeralFlag(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--ephemeral"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !res.Options.Ephemeral {
		t.Fatal("expected ephemeral true")
	}
	if res.Sources.Ephemeral != model.SourceCLI {
		t.Fatalf("expected ephemeral source CLI, got %v", res.Sources.Ephemeral)
	}
}

func TestParseAddReadonlyDirFlag(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--add-readonly-dir", "/tmp/readonly"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !reflect.DeepEqual(res.Options.AddReadonlyDirs, []string{"/tmp/readonly"}) {
		t.Fatalf("expected add readonly dirs to contain /tmp/readonly, got %v", res.Options.AddReadonlyDirs)
	}
	if res.Sources.AddReadonlyDirs != model.SourceCLI {
		t.Fatalf("expected add readonly dirs source CLI, got %v", res.Sources.AddReadonlyDirs)
	}
}

func TestParseProjectMountFlag(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--project-mount", "readonly"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Options.ProjectMount != model.ProjectMountReadonly {
		t.Fatalf("expected project mount readonly, got %q", res.Options.ProjectMount)
	}
	if res.Sources.ProjectMount != model.SourceCLI {
		t.Fatalf("expected project mount source CLI, got %v", res.Sources.ProjectMount)
	}
}

func TestParseWorktreeMetadataFlag(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--worktree-metadata", "none"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Options.WorktreeMetadata != model.WorktreeMetadataNone {
		t.Fatalf("expected worktree metadata none, got %q", res.Options.WorktreeMetadata)
	}
	if res.Sources.WorktreeMetadata != model.SourceCLI {
		t.Fatalf("expected worktree metadata source CLI, got %v", res.Sources.WorktreeMetadata)
	}
}

func TestParseAllowDomainFlag(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--allow-domain", "api.deepseek.com", "--allow-domain", "api.example.com"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	expected := []string{"api.deepseek.com", "api.example.com"}
	if !reflect.DeepEqual(res.Options.AllowDomains, expected) {
		t.Fatalf("expected allow domains %v, got %v", expected, res.Options.AllowDomains)
	}
	if res.Sources.AllowDomains != model.SourceCLI {
		t.Fatalf("expected allow domains source CLI, got %v", res.Sources.AllowDomains)
	}
}

func TestParseAllowDomainFlagMissingValue(t *testing.T) {
	defaults := config.DefaultOptions()
	_, err := Parse([]string{"--allow-domain"}, defaults)
	if err == nil {
		t.Fatal("expected error for --allow-domain without value")
	}
	if !strings.Contains(err.Error(), "--allow-domain") {
		t.Fatalf("expected error to mention --allow-domain, got: %v", err)
	}
}

func TestParseDevcontainerRun(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"devcontainer", "run", "claude"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "run" {
		t.Fatalf("expected action run, got %s", res.Action)
	}
	if !res.Options.Devcontainer {
		t.Fatal("expected devcontainer true")
	}
	if res.Sources.Devcontainer != model.SourceCLI {
		t.Fatalf("expected devcontainer source CLI, got %v", res.Sources.Devcontainer)
	}
	expected := []string{"claude"}
	if !reflect.DeepEqual(res.Options.CmdArgs, expected) {
		t.Fatalf("expected cmd args %v, got %v", expected, res.Options.CmdArgs)
	}
}

func TestParseDevcontainerRunWithFlags(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"devcontainer", "run", "--tool", "codex", "--rebuild", "--force-base-image"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "run" {
		t.Fatalf("expected action run, got %s", res.Action)
	}
	if !res.Options.Devcontainer {
		t.Fatal("expected devcontainer true")
	}
	if res.Options.Tool != "codex" {
		t.Fatalf("expected tool codex, got %s", res.Options.Tool)
	}
	if !res.Options.ForceRebuild {
		t.Fatal("expected rebuild true")
	}
	if !res.Options.ForceBaseImage {
		t.Fatal("expected force base image true")
	}
}

func TestParseNoRebuildFlag(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--no-rebuild"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !res.Options.NoRebuild {
		t.Fatal("expected no-rebuild true")
	}
	if res.Sources.NoRebuild != model.SourceCLI {
		t.Fatalf("expected no-rebuild source CLI, got %v", res.Sources.NoRebuild)
	}
}

func TestParseDevcontainerFlagRemoved(t *testing.T) {
	defaults := config.DefaultOptions()
	_, err := Parse([]string{"--devcontainer"}, defaults)
	if err == nil {
		t.Fatal("expected error for removed --devcontainer flag")
	}
}

func TestParseDevcontainerRunDelimiter(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"devcontainer", "run", "--", "--flag", "value"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "run" {
		t.Fatalf("expected action run, got %s", res.Action)
	}
	if !res.Options.Devcontainer {
		t.Fatal("expected devcontainer true")
	}
	expected := []string{"--flag", "value"}
	if !reflect.DeepEqual(res.Options.CmdArgs, expected) {
		t.Fatalf("expected cmd args %v, got %v", expected, res.Options.CmdArgs)
	}
}

func TestParseDevcontainerShell(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"devcontainer", "shell"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "shell" {
		t.Fatalf("expected action shell, got %s", res.Action)
	}
	if !res.Options.Devcontainer {
		t.Fatal("expected devcontainer true")
	}
	if !res.Options.Shell {
		t.Fatal("expected shell true")
	}
	if res.Sources.Devcontainer != model.SourceCLI {
		t.Fatalf("expected devcontainer source CLI, got %v", res.Sources.Devcontainer)
	}
}

func TestParseDevcontainerShellWithArgsAndAdmin(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"devcontainer", "shell", "--admin", "--", "ls", "-la"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "shell" {
		t.Fatalf("expected action shell, got %s", res.Action)
	}
	if !res.Options.Devcontainer {
		t.Fatal("expected devcontainer true")
	}
	if !res.Options.Shell {
		t.Fatal("expected shell true")
	}
	if !res.Options.Admin {
		t.Fatal("expected admin true")
	}
	expected := []string{"ls", "-la"}
	if !reflect.DeepEqual(res.Options.CmdArgs, expected) {
		t.Fatalf("expected cmd args %v, got %v", expected, res.Options.CmdArgs)
	}
}

func TestParsePassEnv(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--pass-env", "FOO,BAR"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	expected := []string{"FOO", "BAR"}
	if !reflect.DeepEqual(res.Options.PassEnv, expected) {
		t.Fatalf("expected pass env %v, got %v", expected, res.Options.PassEnv)
	}
}

func TestParseFeatures(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--features", "node-dev,python-dev", "--features", "python-dev,github-cli"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	expected := []string{"node-dev", "python-dev", "github-cli"}
	if !reflect.DeepEqual(res.Options.Features, expected) {
		t.Fatalf("expected features %v, got %v", expected, res.Options.Features)
	}
	if res.Sources.Features != model.SourceCLI {
		t.Fatalf("expected features source CLI, got %v", res.Sources.Features)
	}
}

func TestParseFeaturesNone(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--features", "none"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Options.Features == nil {
		t.Fatalf("expected explicit empty features slice, got nil")
	}
	if len(res.Options.Features) != 0 {
		t.Fatalf("expected no features, got %v", res.Options.Features)
	}
}

func TestParseFeaturesNoneCannotBeCombined(t *testing.T) {
	defaults := config.DefaultOptions()
	_, err := Parse([]string{"--features", "none,node-dev"}, defaults)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if got := err.Error(); !strings.Contains(got, "cannot be combined") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestParseSubcommandOptionFlags guards against the regression class where a
// subcommand's handler reads an option the command no longer registers (these
// were root-persistent flags on main).
func TestParseSubcommandOptionFlags(t *testing.T) {
	defaults := config.DefaultOptions()
	for _, tc := range []struct {
		name   string
		args   []string
		action string
		check  func(t *testing.T, res Result)
	}{
		{
			name:   "info --tool",
			args:   []string{"info", "--tool", "codex"},
			action: "info",
			check: func(t *testing.T, res Result) {
				if res.Options.Tool != "codex" {
					t.Fatalf("expected tool codex, got %s", res.Options.Tool)
				}
			},
		},
		{
			name:   "info --slim --image-name",
			args:   []string{"info", "--slim", "--image-name", "custom:tag"},
			action: "info",
			check: func(t *testing.T, res Result) {
				if !res.Options.Slim {
					t.Fatal("expected slim true")
				}
				if res.Options.ImageName != "custom:tag" {
					t.Fatalf("expected image name custom:tag, got %s", res.Options.ImageName)
				}
			},
		},
		{
			name:   "tools --tool",
			args:   []string{"tools", "--tool", "codex"},
			action: "tools",
			check: func(t *testing.T, res Result) {
				if res.Options.Tool != "codex" {
					t.Fatalf("expected tool codex, got %s", res.Options.Tool)
				}
			},
		},
		{
			name:   "features --slim",
			args:   []string{"features", "--slim"},
			action: "features",
			check: func(t *testing.T, res Result) {
				if !res.Options.Slim {
					t.Fatal("expected slim true")
				}
			},
		},
		{
			name:   "features --features",
			args:   []string{"features", "--features", "node-dev"},
			action: "features",
			check: func(t *testing.T, res Result) {
				expected := []string{"node-dev"}
				if !reflect.DeepEqual(res.Options.Features, expected) {
					t.Fatalf("expected features %v, got %v", expected, res.Options.Features)
				}
			},
		},
		{
			name:   "devcontainer generate build flags",
			args:   []string{"devcontainer", "generate", "--slim", "--base-image", "ubuntu:24.04", "--image-name", "custom:tag", "--features", "node-dev"},
			action: "devcontainer-generate",
			check: func(t *testing.T, res Result) {
				if !res.Options.Slim {
					t.Fatal("expected slim true")
				}
				if res.Options.BaseImage != "ubuntu:24.04" {
					t.Fatalf("expected base image ubuntu:24.04, got %s", res.Options.BaseImage)
				}
				if res.Options.ImageName != "custom:tag" {
					t.Fatalf("expected image name custom:tag, got %s", res.Options.ImageName)
				}
				expected := []string{"node-dev"}
				if !reflect.DeepEqual(res.Options.Features, expected) {
					t.Fatalf("expected features %v, got %v", expected, res.Options.Features)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.args, defaults)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if res.Action != tc.action {
				t.Fatalf("expected action %s, got %s", tc.action, res.Action)
			}
			tc.check(t, res)
		})
	}
}

func TestParseNetworkLog(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--network-log", "requests"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Options.NetworkLog != model.NetworkLogRequests {
		t.Fatalf("expected network log %q, got %q", model.NetworkLogRequests, res.Options.NetworkLog)
	}
	if res.Sources.NetworkLog != model.SourceCLI {
		t.Fatalf("expected network log source CLI, got %v", res.Sources.NetworkLog)
	}
}

func TestParseRemovedNetworkLogAliases(t *testing.T) {
	defaults := config.DefaultOptions()
	for _, flag := range []string{"--mitm-all", "--dns-log"} {
		if _, err := Parse([]string{flag}, defaults); err == nil {
			t.Fatalf("expected error for removed flag %s", flag)
		}
	}
}

func TestParseNetworkStatus(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"network", "status"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "network-status" {
		t.Fatalf("expected action network-status, got %s", res.Action)
	}
}

func TestParseNetworkPrint(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"network", "print", "--tool", "claude"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "network-print" {
		t.Fatalf("expected action network-print, got %s", res.Action)
	}
	if res.Options.Tool != "claude" {
		t.Fatalf("expected tool claude, got %s", res.Options.Tool)
	}
}

func TestParseExtensionList(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"extension", "list"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "extension-list" {
		t.Fatalf("expected action extension-list, got %s", res.Action)
	}
}

func TestParseNetworkDiff(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"network", "diff"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "network-diff" {
		t.Fatalf("expected action network-diff, got %s", res.Action)
	}
}

func TestParseNetworkAddDomainGlobal(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"network", "add-domain", "example.com", "--global"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "network-add-domain" {
		t.Fatalf("expected action network-add-domain, got %s", res.Action)
	}
	if len(res.Options.CmdArgs) != 1 || res.Options.CmdArgs[0] != "example.com" {
		t.Fatalf("expected cmd args [example.com], got %v", res.Options.CmdArgs)
	}
}

func TestParseNetworkAddDomainMissingScope(t *testing.T) {
	defaults := config.DefaultOptions()
	_, err := Parse([]string{"network", "add-domain", "example.com"}, defaults)
	if err == nil {
		t.Fatal("expected error when --global not specified")
	}
}

func TestParseRemovedCommandsAndUsageFlags(t *testing.T) {
	defaults := config.DefaultOptions()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "setup", args: []string{"setup"}},
		// "status" is no longer in this list: it was reintroduced as the
		// session-snapshot command, unrelated to the removed usage-status
		// command.
		{name: "keep-usage", args: []string{"cleanup", "--keep-usage"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.args, defaults); err == nil {
				t.Fatalf("expected error for removed CLI args %v", tc.args)
			}
		})
	}
}

func TestParseNetworkAddDomainProjectError(t *testing.T) {
	defaults := config.DefaultOptions()
	_, err := Parse([]string{"network", "add-domain", "example.com", "--project"}, defaults)
	if err == nil {
		t.Fatal("expected error for --project")
	}
}

func TestParseNetworkRemoveDomainGlobal(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"network", "remove-domain", "example.com", "--global"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "network-remove-domain" {
		t.Fatalf("expected action network-remove-domain, got %s", res.Action)
	}
}

func TestParseNetworkSetModeGlobal(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"network", "set-mode", "unrestricted", "--global"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "network-set-mode" {
		t.Fatalf("expected action network-set-mode, got %s", res.Action)
	}
	if len(res.Options.CmdArgs) != 1 || res.Options.CmdArgs[0] != "unrestricted" {
		t.Fatalf("expected cmd args [unrestricted], got %v", res.Options.CmdArgs)
	}
}

func TestParseNetworkSetModeInvalid(t *testing.T) {
	defaults := config.DefaultOptions()
	_, err := Parse([]string{"network", "set-mode", "invalid", "--global"}, defaults)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

// TestParseNetworkToolFlag guards against a regression where apply and the
// mutation commands dropped --tool while their apply path still scopes gateway
// discovery by opts.Tool, locking them to the default tool.
func TestParseNetworkToolFlag(t *testing.T) {
	defaults := config.DefaultOptions()
	for _, tc := range []struct {
		name   string
		args   []string
		action string
	}{
		{name: "apply", args: []string{"network", "apply", "--tool", "codex"}, action: "network-apply"},
		{name: "add-domain", args: []string{"network", "add-domain", "example.com", "--global", "--tool", "codex"}, action: "network-add-domain"},
		{name: "remove-domain", args: []string{"network", "remove-domain", "example.com", "--global", "--tool", "codex"}, action: "network-remove-domain"},
		{name: "set-mode", args: []string{"network", "set-mode", "restricted", "--global", "--tool", "codex"}, action: "network-set-mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.args, defaults)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if res.Action != tc.action {
				t.Fatalf("expected action %s, got %s", tc.action, res.Action)
			}
			if res.Options.Tool != "codex" {
				t.Fatalf("expected tool codex, got %s", res.Options.Tool)
			}
			if res.Sources.Tool != model.SourceCLI {
				t.Fatalf("expected tool source CLI, got %v", res.Sources.Tool)
			}
		})
	}
}

func TestParseNetworkMigrateRejected(t *testing.T) {
	defaults := config.DefaultOptions()
	_, err := Parse([]string{"network", "migrate"}, defaults)
	if err == nil {
		t.Fatal("expected error for removed network migrate subcommand")
	}
	if !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseBridgePort(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		want       []string
		wantSource bool
		wantErr    bool
	}{
		{name: "single", args: []string{"--bridge-port", "9800"}, want: []string{"9800"}, wantSource: true},
		{name: "multiple flags", args: []string{"--bridge-port", "9800", "--bridge-port", "9801"}, want: []string{"9800", "9801"}},
		{name: "comma separated", args: []string{"--bridge-port", "9800,9801"}, want: []string{"9800", "9801"}},
		{name: "invalid", args: []string{"--bridge-port", "abc"}, wantErr: true},
		{name: "dedupe", args: []string{"--bridge-port", "9800", "--bridge-port", "9800"}, want: []string{"9800"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.args, config.DefaultOptions())
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected parse error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if !reflect.DeepEqual(res.Options.BridgePorts, tc.want) {
				t.Fatalf("expected bridge ports %v, got %v", tc.want, res.Options.BridgePorts)
			}
			if tc.wantSource && res.Sources.BridgePorts != model.SourceCLI {
				t.Fatalf("expected bridge ports source CLI, got %v", res.Sources.BridgePorts)
			}
		})
	}
}

func TestParseCompleteActionIsCompletion(t *testing.T) {
	defaults := config.DefaultOptions()
	for _, cmd := range []string{"__complete", "__completeNoDesc"} {
		t.Run(cmd, func(t *testing.T) {
			res, err := Parse([]string{cmd, ""}, defaults)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if res.Action != "completion" {
				t.Fatalf("expected action completion, got %s", res.Action)
			}
		})
	}
}

// TestNormalizeArgsCompletion pins the rewrite applied to the command line
// carried inside a Cobra completion request. The crucial case is the implicit
// `run`: top-level flag completion must target run, while a bare or partial
// word must still complete subcommand names.
func TestNormalizeArgsCompletion(t *testing.T) {
	tree := commandTree{
		"run":              commandTree{},
		"config":           commandTree{},
		"network":          commandTree{"set-mode": commandTree{}},
		"devcontainer":     commandTree{"run": commandTree{}},
		"help":             commandTree{},
		"__complete":       commandTree{},
		"__completeNoDesc": commandTree{},
	}
	flags := map[string]config.CLIFlag{
		"--tool":      {Name: "--tool", ValueKind: config.CLIValueRequired},
		"--ephemeral": {Name: "--ephemeral", ValueKind: config.CLIValueNone},
	}
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"flag prefix injects implicit run", []string{"__complete", "--"}, []string{"__complete", "run", "--"}},
		{"value flag injects implicit run", []string{"__complete", "--tool", ""}, []string{"__complete", "run", "--tool", ""}},
		{"typed flag then word injects run", []string{"__complete", "--ephemeral", ""}, []string{"__complete", "run", "--ephemeral", ""}},
		{"bare word lists subcommands", []string{"__complete", ""}, []string{"__complete", ""}},
		{"partial subcommand name preserved", []string{"__complete", "co"}, []string{"__complete", "co"}},
		{"explicit subcommand untouched", []string{"__complete", "run", "--"}, []string{"__complete", "run", "--"}},
		{"flag before nested command reorders", []string{"__complete", "--ephemeral", "devcontainer", "run", "--"}, []string{"__complete", "devcontainer", "run", "--ephemeral", "--"}},
		{"nested subcommand untouched", []string{"__complete", "network", "set-mode", ""}, []string{"__complete", "network", "set-mode", ""}},
		{"NoDesc variant injects run", []string{"__completeNoDesc", "--"}, []string{"__completeNoDesc", "run", "--"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeArgs(tc.args, tree, flags)
			if err != nil {
				t.Fatalf("normalizeArgs(%v): %v", tc.args, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("normalizeArgs(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// TestParseCompletionAtRoot is the end-to-end guard for the regression: because
// `enclave` defaults to `run`, top-level `--<TAB>` must complete run's flags,
// while a bare `<TAB>` must still list subcommands.
func TestParseCompletionAtRoot(t *testing.T) {
	flags := captureStdout(t, "__complete", "--")
	for _, want := range []string{"--ephemeral", "--yolo", "--tool"} {
		if !strings.Contains(flags, want) {
			t.Fatalf("top-level flag completion missing %q; got:\n%s", want, flags)
		}
	}

	subcommands := captureStdout(t, "__complete", "")
	for _, want := range []string{"run", "config", "network"} {
		if !strings.Contains(subcommands, want) {
			t.Fatalf("bare completion missing subcommand %q; got:\n%s", want, subcommands)
		}
	}
	if strings.Contains(subcommands, "--ephemeral") {
		t.Fatalf("bare completion should list subcommands, not run flags; got:\n%s", subcommands)
	}
}

// captureStdout runs Parse with the given args and returns what Cobra writes to
// stdout (e.g. completion results or --help text). The output is small, so a
// synchronous pipe read is safe.
func captureStdout(t *testing.T, args ...string) string {
	t.Helper()
	return captureStdoutCmds(t, nil, args...)
}

func TestParseUserCommandVerbatimArgs(t *testing.T) {
	defaults := config.DefaultOptions()
	cmds := []usercmd.Command{{Name: "deploy", Path: "/home/u/.enclave/commands/host/deploy", Target: usercmd.TargetHost}}
	res, err := Parse([]string{"deploy", "--env", "prod"}, defaults, cmds...)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "user-command" {
		t.Fatalf("expected action user-command, got %s", res.Action)
	}
	if res.UserCommand == nil || res.UserCommand.Name != "deploy" {
		t.Fatalf("expected UserCommand deploy, got %+v", res.UserCommand)
	}
	if !reflect.DeepEqual(res.UserCommandArgs, []string{"--env", "prod"}) {
		t.Fatalf("expected verbatim args [--env prod], got %v", res.UserCommandArgs)
	}
}

func TestParseUserCommandHelpReachesScript(t *testing.T) {
	defaults := config.DefaultOptions()
	cmds := []usercmd.Command{{Name: "deploy", Path: "/p/deploy", Target: usercmd.TargetHost}}
	res, err := Parse([]string{"deploy", "--help"}, defaults, cmds...)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "user-command" {
		t.Fatalf("expected action user-command, got %s", res.Action)
	}
	if res.HelpShown {
		t.Fatal("expected --help to reach the script, not Cobra help")
	}
	if !reflect.DeepEqual(res.UserCommandArgs, []string{"--help"}) {
		t.Fatalf("expected verbatim args [--help], got %v", res.UserCommandArgs)
	}
}

func TestParseUserCommandHelpBeforeNameRendersStubHelp(t *testing.T) {
	cmds := []usercmd.Command{
		{Name: "deploy", Path: "/p/deploy", Target: usercmd.TargetHost},
		{Name: "triage", Path: "/p/triage", Target: usercmd.TargetSession},
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "--help host", args: []string{"--help", "deploy"}, want: "User command (/p/deploy)"},
		{name: "-h host", args: []string{"-h", "deploy"}, want: "User command (/p/deploy)"},
		{name: "--help session", args: []string{"--help", "triage"}, want: "User command (/p/triage)"},
		{name: "verbose then --help", args: []string{"--verbose", "--help", "deploy"}, want: "User command (/p/deploy)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdoutCmds(t, cmds, tc.args...)
			if !strings.Contains(out, tc.want) {
				t.Fatalf("expected stub help containing %q, got:\n%s", tc.want, out)
			}
		})
	}
}

func TestParseUserCommandHelpBeforeNameSetsHelpShown(t *testing.T) {
	defaults := config.DefaultOptions()
	cmds := []usercmd.Command{{Name: "deploy", Path: "/p/deploy", Target: usercmd.TargetHost}}
	orig := os.Stdout
	_, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = writer
	res, parseErr := Parse([]string{"--help", "deploy"}, defaults, cmds...)
	_ = writer.Close()
	os.Stdout = orig
	if parseErr != nil {
		t.Fatalf("parse failed: %v", parseErr)
	}
	if !res.HelpShown {
		t.Fatal("expected HelpShown true so app.Run does not execute the script")
	}
}

func TestParseUserCommandBuiltinCollision(t *testing.T) {
	defaults := config.DefaultOptions()
	cmds := []usercmd.Command{{Name: "run", Path: "/p/run", Target: usercmd.TargetHost}}
	res, err := Parse([]string{"run"}, defaults, cmds...)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "run" {
		t.Fatalf("expected built-in run to win, got action %s", res.Action)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected a shadowing warning")
	}
	if !strings.Contains(res.Warnings[0], "run") || !strings.Contains(res.Warnings[0], "shadowed") {
		t.Fatalf("unexpected warning: %q", res.Warnings[0])
	}
}

func TestParseUserCommandGlobalFlagBeforeName(t *testing.T) {
	defaults := config.DefaultOptions()
	cmds := []usercmd.Command{{Name: "deploy", Path: "/p/deploy", Target: usercmd.TargetHost}}
	res, err := Parse([]string{"--verbose", "deploy", "x"}, defaults, cmds...)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "user-command" {
		t.Fatalf("expected action user-command, got %s", res.Action)
	}
	if !res.Options.Verbose {
		t.Fatal("expected verbose true")
	}
	if !reflect.DeepEqual(res.UserCommandArgs, []string{"x"}) {
		t.Fatalf("expected verbatim args [x], got %v", res.UserCommandArgs)
	}
}

func TestParseUserCommandHostRejectsSessionFlag(t *testing.T) {
	defaults := config.DefaultOptions()
	cmds := []usercmd.Command{{Name: "deploy", Path: "/p/deploy", Target: usercmd.TargetHost}}
	_, err := Parse([]string{"--tool", "codex", "deploy"}, defaults, cmds...)
	if err == nil {
		t.Fatal("expected error for --tool before a host command")
	}
	if !strings.Contains(err.Error(), "host commands") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseUserCommandSessionAcceptsSessionFlag(t *testing.T) {
	defaults := config.DefaultOptions()
	cmds := []usercmd.Command{{Name: "triage", Path: "/p/triage", Target: usercmd.TargetSession}}
	res, err := Parse([]string{"--tool", "codex", "triage", "arg"}, defaults, cmds...)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "user-command" {
		t.Fatalf("expected action user-command, got %s", res.Action)
	}
	if res.Options.Tool != "codex" {
		t.Fatalf("expected tool codex, got %s", res.Options.Tool)
	}
	if !reflect.DeepEqual(res.UserCommandArgs, []string{"arg"}) {
		t.Fatalf("expected verbatim args [arg], got %v", res.UserCommandArgs)
	}
}

func TestParseUnknownNameStillErrorsWithoutUserCommands(t *testing.T) {
	defaults := config.DefaultOptions()
	_, err := Parse([]string{"deploy"}, defaults)
	if err == nil {
		t.Fatal("expected unknown command error")
	}
	if !strings.Contains(err.Error(), "unknown command") || !strings.Contains(err.Error(), "deploy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// captureStdoutCmds runs Parse with the given user commands and args and
// returns what Cobra writes to stdout (help or completion output). The output
// is small, so a synchronous pipe read is safe.
func captureStdoutCmds(t *testing.T, cmds []usercmd.Command, args ...string) string {
	t.Helper()
	orig := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = writer
	_, parseErr := Parse(args, config.DefaultOptions(), cmds...)
	_ = writer.Close()
	os.Stdout = orig
	if parseErr != nil {
		t.Fatalf("Parse(%v): %v", args, parseErr)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

func TestParseUserCommandHelpSection(t *testing.T) {
	cmds := []usercmd.Command{{Name: "deploy", Path: "/p/deploy", Target: usercmd.TargetHost}}
	help := captureStdoutCmds(t, cmds, "--help")
	if !strings.Contains(help, "User Commands:") {
		t.Fatalf("expected User Commands section, got:\n%s", help)
	}
	if !strings.Contains(help, "deploy") {
		t.Fatalf("expected deploy listed, got:\n%s", help)
	}
	// The no-op Run makes the stub an available command, so it must be grouped
	// under "User Commands:" rather than demoted to help topics.
	if strings.Contains(help, "Additional help topics:") {
		t.Fatalf("user command should not appear under Additional help topics, got:\n%s", help)
	}
}

// TestParseUserCommandCompletion pins that user command names autocomplete via
// Cobra's __complete: the no-op Run on the stub makes IsAvailableCommand() true
// so the name is offered.
func TestParseUserCommandCompletion(t *testing.T) {
	cmds := []usercmd.Command{{Name: "deploy", Path: "/p/deploy", Target: usercmd.TargetHost}}
	out := captureStdoutCmds(t, cmds, "__complete", "dep")
	if !strings.Contains(out, "deploy") {
		t.Fatalf("expected completion to offer deploy, got:\n%s", out)
	}
}

func TestParseHelpCommand(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"help", "run"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !res.HelpShown {
		t.Fatal("expected help to be shown")
	}
}

func TestParseLeadingRunFlagHelpUsesImplicitRun(t *testing.T) {
	for _, args := range [][]string{
		{"--yolo", "--help"},
		{"--tool", "codex", "--help"},
		{"-p", "8080", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			help := captureStdout(t, args...)
			if !strings.Contains(help, "enclave run") {
				t.Fatalf("expected implicit run help, got:\n%s", help)
			}
			if !strings.Contains(help, "--tool") {
				t.Fatalf("expected run help to include --tool, got:\n%s", help)
			}
		})
	}
}

func TestParseRootHelpStaysRootHelp(t *testing.T) {
	help := captureStdout(t, "--help")
	if !strings.Contains(help, "Available Commands:") {
		t.Fatalf("expected root help, got:\n%s", help)
	}
}

// TestRunHelpOmitsRemovedFlags asserts the decluttered CLI surface:
// --network-log is still documented, while the removed flags and aliases
// (--build-backend, --mitm-all, --dns-log) do not appear in --help.
func TestRunHelpOmitsRemovedFlags(t *testing.T) {
	help := captureStdout(t, "run", "--help")
	if !strings.Contains(help, "--network-log") {
		t.Fatalf("run --help should list --network-log; got:\n%s", help)
	}
	for _, absent := range []string{"--build-backend", "--mitm-all", "--dns-log"} {
		if strings.Contains(help, absent) {
			t.Fatalf("run --help should not list %q; got:\n%s", absent, help)
		}
	}
}

func TestParseFlagsBeforeExplicitCommand(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--ephemeral", "run"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "run" {
		t.Fatalf("expected action run, got %s", res.Action)
	}
	if !res.Options.Ephemeral {
		t.Fatal("expected ephemeral true")
	}
}

func TestParseFlagsWithValueBeforeExplicitCommand(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--pass-env", "FOO,BAR", "run"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "run" {
		t.Fatalf("expected action run, got %s", res.Action)
	}
	expected := []string{"FOO", "BAR"}
	if !reflect.DeepEqual(res.Options.PassEnv, expected) {
		t.Fatalf("expected pass env %v, got %v", expected, res.Options.PassEnv)
	}
}

func TestParseExecName(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"exec", "--name", "main"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "exec" {
		t.Fatalf("expected action exec, got %s", res.Action)
	}
	if res.Options.SessionName != "main" {
		t.Fatalf("expected session name main, got %s", res.Options.SessionName)
	}
}

func TestParseStopName(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"stop", "--name", "main"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "stop" {
		t.Fatalf("expected action stop, got %s", res.Action)
	}
	if res.Options.SessionName != "main" {
		t.Fatalf("expected session name main, got %s", res.Options.SessionName)
	}
}

func TestParseFlagsBeforeNestedCommand(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--ephemeral", "devcontainer", "run"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "run" {
		t.Fatalf("expected action run, got %s", res.Action)
	}
	if !res.Options.Devcontainer {
		t.Fatal("expected devcontainer true")
	}
	if !res.Options.Ephemeral {
		t.Fatal("expected ephemeral true")
	}
}

func TestParseValueFlagBeforeNestedCommand(t *testing.T) {
	defaults := config.DefaultOptions()
	res, err := Parse([]string{"--tool", "codex", "devcontainer", "run"}, defaults)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.Action != "run" {
		t.Fatalf("expected action run, got %s", res.Action)
	}
	if res.Options.Tool != "codex" {
		t.Fatalf("expected tool codex, got %s", res.Options.Tool)
	}
}
