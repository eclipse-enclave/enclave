// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/mounts"
)

func appWorktreeFixture(t *testing.T) (main, linked model.Project) {
	t.Helper()
	root := t.TempDir()
	mainDir, linkedDir := filepath.Join(root, "repo"), filepath.Join(root, "linked")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git(root, "init", mainDir)
	git(mainDir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	git(mainDir, "worktree", "add", "-b", "linked", linkedDir)
	var err error
	main, err = config.ResolveProjectFromDir(mainDir)
	if err != nil {
		t.Fatal(err)
	}
	linked, err = config.ResolveProjectFromDir(linkedDir)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func writeWorktreeInput(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigJSONWorktreeSourceAndIdentity(t *testing.T) {
	input := configTestInput(t, testPathsWithFeatures(t), config.DefaultOptions(), nil)
	main, linked := appWorktreeFixture(t)
	home, err := config.ResolveHostHome()
	if err != nil {
		t.Fatal(err)
	}
	globalPath, err := config.GlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	writeWorktreeInput(t, globalPath, `{"worktree_inheritance":true}`)
	path := config.HostProjectConfigJSONPath(home, main.Hash)
	writeWorktreeInput(t, path, `{"tool":"claude","ports":["3000"]}`)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"tool":"codex"}`)
	global, defaults, resolved, warnings, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "config"})
	if err != nil {
		t.Fatal(err)
	}
	input.Ctx = &AppContext{Paths: input.Ctx.Paths, ProjectDir: linked.Dir, project: resolved, projectResolved: true}
	input.GlobalDefaults, input.ProjectDefaults, input.ConfigWarnings = global, defaults, warnings
	input.Options, _, _ = config.ResolveOptionsForTool(input.Options, input.Options.Sources, global, defaults, "")
	doc := runConfigJSON(t, input)
	project := doc["project"].(map[string]any)
	source := doc["configSource"].(map[string]any)
	files := doc["files"].(map[string]any)
	if project["hash"] != linked.Hash || source["hash"] != main.Hash || source["dir"] != main.RealDir || files["project"].(map[string]any)["path"] != path {
		t.Fatalf("wrong identity/source: %+v", doc)
	}
	if doc["schemaVersion"] != "1" || doc["kind"] != "config" {
		t.Fatal("contract changed")
	}
	foundIgnored := false
	for _, item := range doc["warnings"].([]any) {
		warning := item.(map[string]any)
		foundIgnored = foundIgnored || warning["code"] == config.WarningWorktreeOverridesIgnored
	}
	if !foundIgnored {
		t.Fatalf("missing stable ignored-overrides warning: %+v", doc["warnings"])
	}
}

func TestConfigJSONReportsCheckoutInheritanceSelector(t *testing.T) {
	input := configTestInput(t, testPathsWithFeatures(t), config.DefaultOptions(), nil)
	main, linked := appWorktreeFixture(t)
	home, err := config.ResolveHostHome()
	if err != nil {
		t.Fatal(err)
	}
	mainPath, ownPath := config.HostProjectConfigJSONPath(home, main.Hash), config.HostProjectConfigJSONPath(home, linked.Hash)
	writeWorktreeInput(t, mainPath, `{"tool":"claude","worktree_inheritance":false}`)
	writeWorktreeInput(t, ownPath, `{"worktree_inheritance":true}`)
	global, defaults, resolved, warnings, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "config"})
	if err != nil {
		t.Fatal(err)
	}
	input.Ctx = &AppContext{Paths: input.Ctx.Paths, ProjectDir: linked.Dir, project: resolved, projectResolved: true}
	input.GlobalDefaults, input.ProjectDefaults, input.ConfigWarnings = global, defaults, warnings
	input.Options, _, _ = config.ResolveOptionsForTool(input.Options, input.Options.Sources, global, defaults, "")
	doc := runConfigJSON(t, input)
	files := doc["files"].(map[string]any)
	option := doc["options"].(map[string]any)["worktree_inheritance"].(map[string]any)
	if files["project"].(map[string]any)["path"] != mainPath || files["checkout"].(map[string]any)["path"] != ownPath || option["value"] != true || option["source"] != "project" || option["layers"].(map[string]any)["project"] != true {
		t.Fatalf("misreported bootstrap selection: %+v %+v", files, option)
	}
}

func TestAttachWorktreeInheritsProjectLayer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	main, linked := appWorktreeFixture(t)
	home, err := config.ResolveHostHome()
	if err != nil {
		t.Fatal(err)
	}
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, main.Hash), `{"session_tint":"#111111"}`)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"session_tint":"#222222"}`)
	session := backend.Session{Tool: "claude", ProjectDir: linked.Dir, ConfigSource: &backend.ConfigSource{Dir: main.RealDir, Hash: main.Hash}}
	run := attachSessionRunOptions(session, model.RunOptions{})
	if run.SessionTint != "#111111" {
		t.Fatalf("tint = %s, want inherited tint", run.SessionTint)
	}
}

func TestSessionConfigPinAndLegacyFallback(t *testing.T) {
	main, linked := appWorktreeFixture(t)
	session := backend.Session{Tool: "claude", ProjectDir: linked.RealDir, ProjectHash: linked.Hash, ConfigSource: &backend.ConfigSource{Dir: main.RealDir, Hash: main.Hash}}
	writeWorktreeInput(t, filepath.Join(linked.RealDir, ".git"), "broken")
	project, err := sessionConfigProject(session)
	if err != nil || project.ConfigHash != main.Hash {
		t.Fatalf("pin changed: %+v %v", project, err)
	}
	session.ConfigSource = nil
	project, err = sessionConfigProject(session)
	if err != nil || project.ConfigHash != linked.Hash {
		t.Fatalf("legacy session inherited new source: %+v %v", project, err)
	}
	session.ConfigSource = &backend.ConfigSource{}
	if _, err := sessionConfigProject(session); err == nil {
		t.Fatal("malformed pin became fallback")
	}
}

func TestInvalidGitMetadataMountWarnsWithoutInheritance(t *testing.T) {
	_, checkout := appWorktreeFixture(t)
	gitFile := filepath.Join(checkout.RealDir, ".git")
	moved := gitFile + "-original"
	if err := os.Rename(gitFile, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, gitFile); err != nil {
		t.Fatal(err)
	}
	var entries []backend.Mount
	var validated []string
	_, stderr := captureOutput(t, func() { mounts.AddWorktree(&entries, checkout, &validated, false, mounts.SensitivePaths{}) })
	if len(entries) != 0 || !strings.Contains(stderr, "symlinked .git file") || !strings.Contains(stderr, gitFile) {
		t.Fatalf("mounts=%+v stderr=%s", entries, stderr)
	}
}

func TestCheckoutLocalPinSurvivesCheckoutChanges(t *testing.T) {
	for _, change := range []string{"remove", "rename", "symlink"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			home, err := config.ResolveHostHome()
			if err != nil {
				t.Fatal(err)
			}
			_, checkout := appWorktreeFixture(t)
			writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, checkout.Hash), `{"session_tint":"#123456","host_env":{"HERDR_AGENT":"local"},"theia":{"preferences":{"editor.fontSize":17}}}`)
			session := backend.Session{Tool: "claude", ProjectDir: checkout.RealDir, ProjectHash: checkout.Hash, ConfigSource: &backend.ConfigSource{Dir: checkout.RealDir, Hash: checkout.Hash}}
			if change == "remove" {
				err = os.RemoveAll(checkout.RealDir)
			} else {
				moved := checkout.RealDir + "-moved"
				err = os.Rename(checkout.RealDir, moved)
				if err == nil && change == "symlink" {
					err = os.Symlink(t.TempDir(), checkout.RealDir)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			project, err := sessionConfigProject(session)
			if err != nil || project.ConfigSourceHash() != checkout.Hash {
				t.Fatalf("local pin=%+v err=%v", project, err)
			}
			run := attachSessionRunOptions(session, model.RunOptions{})
			if run.SessionTint != "#123456" || run.HostEnv["HERDR_AGENT"] != "local" {
				t.Fatalf("lost local attach settings: %+v", run)
			}
			prefs, err := theiaSessionPreferences(home, session, true)
			if err != nil || prefs["editor.fontSize"] != float64(17) {
				t.Fatalf("lost Theia preferences: %+v %v", prefs, err)
			}
			target := backend.GatewayInfo{ProjectHash: checkout.Hash, Tool: "claude", SessionContainer: "session"}
			if hash, err := gatewayConfigHash(pinnedGatewayInspector{session: &session}, target, checkout.RealDir); err != nil || hash != checkout.Hash {
				t.Fatalf("local gateway hash=%s err=%v", hash, err)
			}
		})
	}
}

func TestTheiaNonYoloDoesNotRequirePinnedSource(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "malformed"}[invalid], func(t *testing.T) {
			main, checkout := appWorktreeFixture(t)
			session := backend.Session{Tool: "claude", ProjectDir: checkout.RealDir, ProjectHash: checkout.Hash, ConfigSource: &backend.ConfigSource{Dir: main.RealDir, Hash: main.Hash}}
			if err := os.RemoveAll(main.RealDir); err != nil {
				t.Fatal(err)
			}
			if invalid {
				session.ConfigSource = &backend.ConfigSource{}
			}
			if prefs, err := theiaSessionPreferences(t.TempDir(), session, false); err != nil || prefs != nil {
				t.Fatalf("non-yolo needs no source: %+v %v", prefs, err)
			}
			if _, err := theiaSessionPreferences(t.TempDir(), session, true); err == nil {
				t.Fatal("yolo ignored invalid/unavailable pin")
			}
		})
	}
}

func TestWorktreeCleanupKeepsInheritedState(t *testing.T) {
	home := t.TempDir()
	project := model.Project{Hash: "sibling", ConfigHash: "main"}
	shared := config.HostProjectMemoryDir(home, "main", "claude")
	writeWorktreeInput(t, filepath.Join(shared, "memory.md"), "keep")
	for _, dir := range resolveCleanupDirs(model.RunOptions{Tool: "claude"}, model.CleanupOptions{}, home, project) {
		if strings.Contains(dir.Path, string(filepath.Separator)+"main"+string(filepath.Separator)) {
			t.Fatalf("cleanup follows config hash: %s", dir.Path)
		}
		if err := os.RemoveAll(dir.Path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(shared, "memory.md")); err != nil {
		t.Fatalf("inherited memory removed: %v", err)
	}
}

type pinnedGatewayInspector struct {
	backend.Backend
	session *backend.Session
	err     error
}

func (m pinnedGatewayInspector) Inspect(context.Context, backend.SessionRef) (*backend.Session, error) {
	return m.session, m.err
}

func TestGatewayUsesPinnedConfigSource(t *testing.T) {
	main, linked := appWorktreeFixture(t)
	session := &backend.Session{Tool: "claude", ProjectHash: linked.Hash, ProjectDir: linked.RealDir, ConfigSource: &backend.ConfigSource{Dir: main.RealDir, Hash: main.Hash}}
	manager := pinnedGatewayInspector{session: session}
	target := backend.GatewayInfo{ProjectHash: linked.Hash, Tool: "claude", SessionContainer: "session"}
	// Current metadata is no longer usable; it must not select a new layer.
	writeWorktreeInput(t, filepath.Join(linked.RealDir, ".git"), "broken")
	got, err := gatewayConfigHash(manager, target, linked.RealDir)
	if err != nil {
		t.Fatal(err)
	}
	if got != main.Hash {
		t.Fatalf("config hash = %s, want %s", got, main.Hash)
	}
	target.ProjectHash = "wrong"
	if _, err := gatewayConfigHash(manager, target, linked.RealDir); err == nil {
		t.Fatal("accepted mismatched state identity")
	}
}
