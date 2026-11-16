// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/usercmd"
)

func projectConfigFixture(t *testing.T, separate bool) (home string, main, linked model.Project, metadata string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	globalPath, err := config.GlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	writeWorktreeInput(t, globalPath, `{"worktree_inheritance":true}`)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mainDir, linkedDir := filepath.Join(root, "main"), filepath.Join(root, "linked")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	metadata = filepath.Join(mainDir, ".git")
	if separate {
		metadata = filepath.Join(root, "metadata")
		git(root, "init", "--separate-git-dir", metadata, mainDir)
	} else {
		git(root, "init", mainDir)
	}
	git(mainDir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	git(mainDir, "worktree", "add", "-b", "linked", linkedDir)
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

func TestWorktreeInheritanceDefaultsToCheckoutLayer(t *testing.T) {
	home, main, linked, _ := projectConfigFixture(t, false)
	globalPath, err := config.GlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	writeWorktreeInput(t, globalPath, `{}`)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, main.Hash), `{"tool":"claude","allow_all_network":true}`)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"tool":"codex","allow_all_network":false}`)
	parsed, err := cli.Parse(nil, config.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	global, project, resolved, warnings, err := loadDefaultsForAction(linked.Dir, parsed)
	if err != nil || resolved.ConfigSourceHash() != linked.Hash || len(warnings) != 0 {
		t.Fatalf("default inherited: %+v %+v %v", resolved, warnings, err)
	}
	effective, _, _ := config.ResolveOptionsForTool(parsed.Options, parsed.Sources, global, project, "")
	if effective.WorktreeInheritance || effective.Tool != "codex" || effective.AllowAllNetwork {
		t.Fatalf("default discarded local policy: %+v", effective)
	}
}

func TestWorktreeInheritanceOptOutUsesOnlyCheckoutLayer(t *testing.T) {
	home, main, linked, _ := projectConfigFixture(t, false)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, main.Hash), `{"tool":"claude","host_env":{"MAIN":"yes"}}`)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"tool":"codex","host_env":{"LOCAL":"yes"}}`)
	parsed, err := cli.Parse([]string{"--worktree-inheritance=false"}, config.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, broken := range []bool{false, true} {
		if broken {
			writeWorktreeInput(t, filepath.Join(linked.Dir, ".git"), "gitdir: /missing/worktrees/stale\n")
		}
		global, defaults, resolved, warnings, err := loadDefaultsForAction(linked.Dir, parsed)
		if err != nil || resolved.ConfigSourceDir() != linked.RealDir || resolved.ConfigSourceHash() != linked.Hash {
			t.Fatalf("broken=%v: source=%+v err=%v", broken, resolved, err)
		}
		effective, _, _ := config.ResolveOptionsForTool(parsed.Options, parsed.Sources, global, defaults, "")
		if effective.Tool != "codex" || effective.HostEnv["LOCAL"] != "yes" || effective.HostEnv["MAIN"] != "" || effective.WorktreeInheritance {
			t.Fatalf("mixed checkout/anchor layers: %+v", effective)
		}
		if got := sessionProvenance(effective, model.Profile{Name: effective.Tool}, featureSelection{})["worktree_inheritance"]; got != false {
			t.Fatalf("missing opt-out provenance: %v", got)
		}
		for _, warning := range warnings {
			if warning.Code == config.WarningWorktreeOverridesIgnored || warning.Code == config.WarningConfigAnchorUnavailable || warning.Code == config.WarningInvalidWorktreeMetadata {
				t.Fatalf("opt-out still attempted inheritance: %+v", warnings)
			}
		}
	}
}

func TestWorktreeInheritanceSettingPrecedence(t *testing.T) {
	ptr := func(value bool) *bool { return &value }
	for _, tc := range []struct {
		name                       string
		global, anchor, local, cli *bool
		want                       bool
		source                     model.OptionSource
	}{
		{"default", nil, nil, nil, nil, false, model.SourceDefault},
		{"global false", ptr(false), nil, nil, nil, false, model.SourceGlobal},
		{"global true", ptr(true), nil, nil, nil, true, model.SourceGlobal},
		{"anchor true", nil, ptr(true), nil, nil, true, model.SourceProject},
		{"anchor true over global false", ptr(false), ptr(true), nil, nil, true, model.SourceProject},
		{"anchor false over global true", ptr(true), ptr(false), nil, nil, false, model.SourceProject},
		{"local false", ptr(true), ptr(true), ptr(false), nil, false, model.SourceProject},
		{"local true", ptr(false), ptr(false), ptr(true), nil, true, model.SourceProject},
		{"CLI true", ptr(false), ptr(false), ptr(false), ptr(true), true, model.SourceCLI},
		{"CLI false", ptr(true), ptr(true), ptr(true), ptr(false), false, model.SourceCLI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, main, linked, _ := projectConfigFixture(t, false)
			globalPath, err := config.GlobalConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			globalDoc, localDoc := map[string]any{}, map[string]any{"tool": "codex"}
			// The anchor's tool override may never select the layer.
			anchorDoc := map[string]any{"tool": "claude", "tool_overrides": map[string]any{"claude": map[string]any{"worktree_inheritance": !tc.want}}}
			if tc.anchor != nil {
				anchorDoc["worktree_inheritance"] = *tc.anchor
			}
			if tc.global != nil {
				globalDoc["worktree_inheritance"] = *tc.global
			}
			if tc.local != nil {
				localDoc["worktree_inheritance"] = *tc.local
			}
			for path, doc := range map[string]map[string]any{globalPath: globalDoc, config.HostProjectConfigJSONPath(home, linked.Hash): localDoc, config.HostProjectConfigJSONPath(home, main.Hash): anchorDoc} {
				data, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				writeWorktreeInput(t, path, string(data))
			}
			args := []string{}
			if tc.cli != nil {
				args = append(args, fmt.Sprintf("--worktree-inheritance=%t", *tc.cli))
			}
			parsed, err := cli.Parse(args, config.DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			global, project, resolved, _, err := loadDefaultsForAction(linked.Dir, parsed)
			if err != nil {
				t.Fatal(err)
			}
			effective, _, _ := config.ResolveOptionsForTool(parsed.Options, parsed.Sources, global, project, "")
			wantHash, wantTool := linked.Hash, "codex"
			if tc.want {
				wantHash, wantTool = main.Hash, "claude"
			}
			if resolved.ConfigSourceHash() != wantHash || effective.Tool != wantTool || effective.WorktreeInheritance != tc.want || effective.Sources.WorktreeInheritance != tc.source {
				t.Fatalf("wrong selection: project=%+v inheritance=%v source=%v tool=%s", resolved, effective.WorktreeInheritance, effective.Sources.WorktreeInheritance, effective.Tool)
			}
		})
	}
}

func TestConfiguredLocalSelectionDoesNotRequireWorktreeMetadata(t *testing.T) {
	home, main, linked, _ := projectConfigFixture(t, false)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, main.Hash), `{"image_mode":"removed"}`)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"worktree_inheritance":false,"tool":"codex"}`)
	writeWorktreeInput(t, filepath.Join(linked.Dir, ".git"), "gitdir: /missing/worktrees/stale\n")
	parsed, err := cli.Parse(nil, config.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	global, project, resolved, _, err := loadDefaultsForAction(linked.Dir, parsed)
	if err != nil || resolved.ConfigSourceHash() != linked.Hash {
		t.Fatalf("local setting consulted metadata/anchor: %+v %v", resolved, err)
	}
	effective, _, _ := config.ResolveOptionsForTool(parsed.Options, parsed.Sources, global, project, "")
	if effective.WorktreeInheritance || effective.Tool != "codex" {
		t.Fatalf("local setting lost: %+v", effective)
	}
}

func TestCheckoutConfigWarningsAreNotDuplicated(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		inherit, separate, broken bool
	}{
		{"checkout local", false, false, false},
		{"inherited", true, false, false},
		{"unsupported anchor", true, true, false},
		{"broken metadata inspection", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _, linked, _ := projectConfigFixture(t, tc.separate)
			globalPath, err := config.GlobalConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			writeWorktreeInput(t, globalPath, fmt.Sprintf(`{"worktree_inheritance":%t}`, tc.inherit))
			writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{`)
			if tc.broken {
				writeWorktreeInput(t, filepath.Join(linked.Dir, ".git"), "broken")
			}
			_, _, _, warnings, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "config"})
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, warning := range warnings {
				if warning.Code == config.WarningInvalidConfigJSON {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("want one checkout parse warning, got %+v", warnings)
			}
		})
	}
}

func TestSelectionOnlyCheckoutConfigIsNotIgnored(t *testing.T) {
	home, main, linked, _ := projectConfigFixture(t, false)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"worktree_inheritance":true}`)
	_, _, project, warnings, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "run"})
	if err != nil || project.ConfigSourceHash() != main.Hash || len(warnings) != 0 {
		t.Fatalf("selection-only config: %+v %+v %v", project, warnings, err)
	}
}

func TestWorktreeInheritsProjectLayer(t *testing.T) {
	home, main, linked, _ := projectConfigFixture(t, false)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, main.Hash), `{"tool":"claude", "host_env":{"MAIN":"yes"}}`)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"tool":"codex", "host_env":{"OWN":"yes"}}`)
	global, project, resolved, warnings, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "run"})
	if err != nil {
		t.Fatal(err)
	}
	effective, _, _ := config.ResolveOptionsForTool(config.DefaultOptions(), model.DefaultOptionSources(), global, project, "")
	if resolved.Hash != linked.Hash || resolved.RealDir != linked.RealDir {
		t.Fatalf("state identity changed: %+v", resolved)
	}
	if resolved.ConfigHash != main.Hash || effective.Tool != "claude" || len(effective.HostEnv) != 1 || effective.HostEnv["MAIN"] != "yes" {
		t.Fatalf("mixed config: %+v %+v", resolved, effective)
	}
	if len(warnings) != 1 || warnings[0].Code != config.WarningWorktreeOverridesIgnored || !strings.Contains(warnings[0].Message, "inherits the project layer") {
		t.Fatalf("warnings: %+v", warnings)
	}
}

func TestWorktreeInvalidIgnoredConfigAndUnverifiedCandidate(t *testing.T) {
	home, main, linked, _ := projectConfigFixture(t, false)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, main.Hash), `{"tool":"claude"}`)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"image_mode":"removed"}`)
	if _, _, _, _, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "run"}); err != nil {
		t.Fatalf("ignored own file read: %v", err)
	}
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, main.Hash), `{"image_mode":"removed"}`)
	if _, _, _, _, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "run"}); err == nil {
		t.Fatal("selected broken config accepted")
	}
	attacker := t.TempDir()
	data, err := os.ReadFile(filepath.Join(linked.Dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	writeWorktreeInput(t, filepath.Join(attacker, ".git"), string(data))
	_, project, resolved, _, err := loadDefaultsForAction(attacker, cli.Result{Action: "run"})
	if err == nil || !strings.Contains(err.Error(), "invalid worktree metadata") {
		t.Fatalf("expected metadata error before candidate config read: %v", err)
	}
	if resolved.ConfigHash != resolved.Hash || project.Tool != "" {
		t.Fatalf("unverified inheritance: %+v", resolved)
	}
}

func TestInvalidWorktreeMetadataNeverLoadsOwnPolicy(t *testing.T) {
	for _, contents := range []string{"broken", "gitdir:", "gitdir: /missing/worktrees/stale"} {
		t.Run(contents, func(t *testing.T) {
			home, _, linked, _ := projectConfigFixture(t, false)
			writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"allow_all_network":true}`)
			writeWorktreeInput(t, filepath.Join(linked.Dir, ".git"), contents)
			_, project, _, _, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "run"})
			if err == nil || !strings.Contains(err.Error(), "invalid worktree metadata") || project.AllowAllNetwork != nil {
				t.Fatalf("unsafe fallback: %+v %v", project, err)
			}
		})
	}
}

func TestBrokenWorktreeMetadataDoesNotBlockLifecycle(t *testing.T) {
	home, main, linked, _ := projectConfigFixture(t, false)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, main.Hash), `{"tool":"claude"}`)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"tool":"codex"}`)
	writeWorktreeInput(t, filepath.Join(linked.Dir, ".git"), "gitdir: /missing/worktrees/stale\n")
	for _, action := range []string{"ps", "stop", "attach", "exec", "theia", "network-status", "network-log", "network-apply", "cleanup", "config", "info", "tools", "features", cli.ActionExtensionManage, "network-print", "future-inspection-command"} {
		_, defaults, project, warnings, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: action})
		if err != nil || defaults.Tool != "codex" || project.ConfigHash != linked.Hash || len(warnings) != 1 || warnings[0].Code != config.WarningInvalidWorktreeMetadata {
			t.Fatalf("%s: %+v %+v %+v %v", action, defaults, project, warnings, err)
		}
	}
	for _, action := range []string{"run", "shell", "continue", "resume", "user-command", "devcontainer-generate"} {
		if _, _, _, _, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: action}); err == nil {
			t.Fatalf("%s allowed a broken worktree", action)
		}
	}
	for _, target := range []usercmd.Target{usercmd.TargetHost, usercmd.TargetSession} {
		_, _, _, _, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "user-command", UserCommand: &usercmd.Command{Target: target}})
		if (err == nil) != (target == usercmd.TargetHost) {
			t.Fatalf("user command %s: %v", target, err)
		}
	}
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"image_mode":"removed"}`)
	_, defaults, project, _, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "cleanup", Options: model.Options{CleanupOptions: model.CleanupOptions{CleanupAll: true}}})
	if err != nil || defaults.Tool != "" || project.Hash != "" {
		t.Fatalf("full cleanup consulted project config: %+v %+v %v", defaults, project, err)
	}
}

func TestMovedWorktreeErrorSuggestsRepair(t *testing.T) {
	_, _, linked, _ := projectConfigFixture(t, false)
	moved := linked.Dir + "-moved"
	if err := os.Rename(linked.Dir, moved); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := loadDefaultsForAction(moved, cli.Result{Action: "run"}); err == nil || !strings.Contains(err.Error(), "git worktree repair") {
		t.Fatalf("missing actionable repair guidance: %v", err)
	}
}

func TestPinIdentityValidationMatchesSessionJSON(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hash := config.ProjectHashForPath(dir)
	for _, source := range []backend.ConfigSource{{Dir: dir, Hash: hash}, {Dir: "relative", Hash: hash}, {Dir: dir + "/../bad", Hash: hash}, {Dir: dir, Hash: "wrong"}, {}} {
		_, jsonError := buildPSJSONConfigSource(&source)
		_, lifecycleError := resolvePinnedProjectConfig(model.Project{}, source.Dir, source.Hash)
		if (jsonError == "") != (lifecycleError == nil) {
			t.Fatalf("pin validators disagree for %+v: %s %v", source, jsonError, lifecycleError)
		}
	}
	// Historical identity remains reportable after the source disappears.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if source, err := buildPSJSONConfigSource(&backend.ConfigSource{Dir: dir, Hash: hash}); source == nil || err != "" {
		t.Fatalf("historical identity lost: %+v %s", source, err)
	}
	if _, err := resolvePinnedProjectConfig(model.Project{}, dir, hash); err == nil {
		t.Fatal("lifecycle accepted an unavailable pin")
	}
}

func TestPinnedProjectConfigRejectsChangedAnchor(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project, _ := config.ResolveProjectFromDir(t.TempDir())
	hash := config.ProjectHashForPath(dir)
	pinned, err := resolvePinnedProjectConfig(project, dir, hash)
	if err != nil || pinned.ConfigHash != hash || pinned.Hash != project.Hash {
		t.Fatalf("pin: %+v %v", pinned, err)
	}
	for _, tc := range []struct{ dir, hash string }{{dir, "wrong"}, {"relative", hash}, {"", ""}} {
		if _, err := resolvePinnedProjectConfig(project, tc.dir, tc.hash); err == nil {
			t.Fatalf("accepted malformed pin: %+v", tc)
		}
	}
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	if _, err := resolvePinnedProjectConfig(project, dir, hash); err == nil {
		t.Fatal("accepted missing anchor")
	}
	if err := os.Symlink(moved, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePinnedProjectConfig(project, dir, hash); err == nil {
		t.Fatal("followed replaced anchor symlink")
	}
}

func TestUnanchoredWorktreeFailsInheritingStart(t *testing.T) {
	home, main, linked, metadata := projectConfigFixture(t, true)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, main.Hash), `{"image_mode":"removed"}`)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, config.ProjectHashForPath(metadata)), `{"image_mode":"removed"}`)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"tool":"codex"}`)
	if _, _, _, _, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "run"}); !errors.Is(err, errConfigAnchorUnavailable) || !strings.Contains(err.Error(), "--worktree-inheritance=false") {
		t.Fatalf("unanchored start did not fail with the opt-out: %v", err)
	}
	_, project, resolved, warnings, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "config"})
	if err != nil || resolved.ConfigHash != linked.Hash || project.Tool != "codex" || len(warnings) != 1 || warnings[0].Code != config.WarningConfigAnchorUnavailable || !strings.Contains(warnings[0].Message, "checkout-local") {
		t.Fatalf("inspection fallback: %+v %+v %+v %v", resolved, project, warnings, err)
	}
	parsed, err := cli.Parse([]string{"--worktree-inheritance=false"}, config.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	_, project, resolved, warnings, err = loadDefaultsForAction(linked.Dir, parsed)
	if err != nil || resolved.ConfigSourceHash() != linked.Hash || project.Tool != "codex" || len(warnings) != 0 {
		t.Fatalf("explicit opt-out: %+v %+v %+v %v", resolved, project, warnings, err)
	}
	writeWorktreeInput(t, filepath.Join(metadata, "worktrees", "linked", "gitdir"), "/missing/.git")
	if _, _, _, _, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "run"}); !errors.Is(err, errInvalidWorktreeMetadata) {
		t.Fatalf("invalid relationship: %v", err)
	}
}

// An anchor that opted in still counts as a request when repository config
// later stops establishing it, so the start fails without any global opt-in.
func TestAnchorOptInSurvivesUnsupportedRepositoryConfig(t *testing.T) {
	home, main, linked, metadata := projectConfigFixture(t, false)
	globalPath, _ := config.GlobalConfigPath()
	writeWorktreeInput(t, globalPath, `{}`)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, main.Hash), `{"tool":"claude","worktree_inheritance":true}`)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"tool":"codex"}`)
	_, project, resolved, _, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "run"})
	if err != nil || resolved.ConfigHash != main.Hash || project.Tool != "claude" {
		t.Fatalf("anchor opt-in not applied: %+v %+v %v", resolved, project, err)
	}
	writeWorktreeInput(t, filepath.Join(metadata, "config"), "[core]\n bare = false\n[include]\n path = /missing\n")
	if _, _, _, _, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "run"}); !errors.Is(err, errConfigAnchorUnavailable) {
		t.Fatalf("lost anchor became local fallback: %v", err)
	}
}

// An unreadable anchor config must not turn into a silent local fallback.
func TestInvalidAnchorConfigWarnsOnLocalFallback(t *testing.T) {
	home, main, linked, _ := projectConfigFixture(t, false)
	globalPath, _ := config.GlobalConfigPath()
	writeWorktreeInput(t, globalPath, `{}`)
	anchorPath := config.HostProjectConfigJSONPath(home, main.Hash)
	writeWorktreeInput(t, anchorPath, `{"worktree_inheritance":true,`)
	_, _, resolved, warnings, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "run"})
	if err != nil || resolved.ConfigSourceHash() != linked.Hash {
		t.Fatalf("unexpected source: %+v %v", resolved, err)
	}
	if len(warnings) != 1 || warnings[0].Code != config.WarningInvalidConfigJSON || warnings[0].Path != anchorPath {
		t.Fatalf("anchor parse error not reported: %+v", warnings)
	}
}

// The main checkout's own setting only governs its worktrees.
func TestAnchorSettingDoesNotAffectMainCheckout(t *testing.T) {
	home, main, _, _ := projectConfigFixture(t, false)
	writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, main.Hash), `{"tool":"claude","worktree_inheritance":true}`)
	_, project, resolved, warnings, err := loadDefaultsForAction(main.Dir, cli.Result{Action: "run"})
	if err != nil || resolved.ConfigSourceHash() != main.Hash || project.Tool != "claude" || len(warnings) != 0 {
		t.Fatalf("main checkout: %+v %+v %+v %v", resolved, project, warnings, err)
	}
}

func TestNormalRepositoryConfigDoesNotBlockWorktreeStartup(t *testing.T) {
	large := "[core]\n bare = false\n"
	for i := range 100 {
		large += fmt.Sprintf("[branch \"topic-%d\"]\n remote = origin\n merge = refs/heads/topic-%d\n", i, i)
	}
	for _, tc := range []struct {
		name, contents string
		inherited      bool
	}{
		{"large tracking config", large, true},
		{"inline comments", "[core] # comment\n bare = false ; comment\n", true},
		{"local includes", "[core]\n bare = false\n[include]\n path = /missing\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, main, linked, metadata := projectConfigFixture(t, false)
			writeWorktreeInput(t, filepath.Join(metadata, "config"), tc.contents)
			writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, main.Hash), `{"tool":"claude"}`)
			writeWorktreeInput(t, config.HostProjectConfigJSONPath(home, linked.Hash), `{"tool":"codex"}`)
			_, defaults, resolved, warnings, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "run"})
			if !tc.inherited {
				if !errors.Is(err, errConfigAnchorUnavailable) {
					t.Fatalf("unsupported config became local fallback: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if defaults.Tool != "claude" || resolved.ConfigHash != main.Hash || len(warnings) != 1 || warnings[0].Code != config.WarningWorktreeOverridesIgnored {
				t.Fatalf("incorrect config resolution: %+v %+v %+v", defaults, resolved, warnings)
			}
		})
	}
}

func TestIgnoredWorktreeOverrides(t *testing.T) {
	for _, name := range []string{"config.json", "network.jsonc", "secrets.env", "gateway-allowlists/claude", "patches/claude/settings.json", "claude/config/settings.json", "skills/example/SKILL.md"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeWorktreeInput(t, filepath.Join(root, "project.json"), `{}`)
			if config.HasUserOverrides(root) {
				t.Fatal("marker reported as config")
			}
			writeWorktreeInput(t, filepath.Join(root, name), "data")
			if !config.HasUserOverrides(root) {
				t.Fatal("missed user config")
			}
		})
	}
}

func TestIgnoredWorktreeWarningNamesOnlyPopulatedDirectories(t *testing.T) {
	for _, tc := range []struct {
		name               string
		overrides, secrets bool
	}{
		{"overrides", true, false}, {"secrets", false, true}, {"both", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _, linked, _ := projectConfigFixture(t, false)
			own, secretDir := config.HostProjectOverridesDir(home, linked.Hash), config.HostSecretsProjectDir(home, linked.Hash)
			if tc.overrides {
				writeWorktreeInput(t, filepath.Join(own, "config.json"), `{"tool":"codex"}`)
			}
			if tc.secrets {
				writeWorktreeInput(t, filepath.Join(secretDir, "claude.env"), "API_KEY=hidden")
			}
			_, _, _, warnings, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "run"})
			if err != nil || len(warnings) != 1 {
				t.Fatalf("warnings=%+v err=%v", warnings, err)
			}
			warning := warnings[0]
			if strings.Contains(warning.Message, own) != tc.overrides || strings.Contains(warning.Message, secretDir) != tc.secrets || strings.Contains(warning.Message, "hidden") {
				t.Fatalf("wrong ignored paths: %+v", warning)
			}
			wantPath := own
			if !tc.overrides {
				wantPath = secretDir
			}
			if warning.Path != wantPath {
				t.Fatalf("warning path=%s, want %s", warning.Path, wantPath)
			}
		})
	}
}

func TestIgnoredWorktreeSecretsWarningDoesNotExposeValues(t *testing.T) {
	home, _, linked, _ := projectConfigFixture(t, false)
	writeWorktreeInput(t, config.HostSecretsProjectFile(home, linked.Hash, "claude"), "API_KEY=do-not-print\n")
	_, _, _, warnings, err := loadDefaultsForAction(linked.Dir, cli.Result{Action: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || warnings[0].Code != config.WarningWorktreeOverridesIgnored || !strings.Contains(warnings[0].Message, "project secrets") || strings.Contains(warnings[0].Message, "do-not-print") {
		t.Fatalf("warnings: %+v", warnings)
	}
}
