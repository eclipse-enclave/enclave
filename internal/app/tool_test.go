// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/extinstall"
	"enclave/internal/model"
)

// toolPromptStub records what the stubbed tool-resolution seams were asked and
// told to save.
type toolPromptStub struct {
	Offered []string
	Saved   []string
}

// stubToolResolution replaces the tool-resolution seams for one test: the
// installed agents, whether a question could be shown, what the user answers,
// and where the answer is saved.
func stubToolResolution(t *testing.T, tools []string, interactive bool, answer string) *toolPromptStub {
	t.Helper()
	previousList, previousPrompt, previousChoose, previousSave := listAgentTools, promptUsable, chooseTool, saveToolChoice
	t.Cleanup(func() {
		listAgentTools, promptUsable, chooseTool, saveToolChoice = previousList, previousPrompt, previousChoose, previousSave
	})
	stub := &toolPromptStub{}
	listAgentTools = func() []string { return tools }
	promptUsable = func() bool { return interactive }
	chooseTool = func(_ string, options []string) (string, error) {
		stub.Offered = append(stub.Offered, strings.Join(options, ","))
		return answer, nil
	}
	saveToolChoice = func(key string, value string) (string, error) {
		stub.Saved = append(stub.Saved, key+"="+value)
		return "config.json", nil
	}
	return stub
}

func TestResolveToolKeepsAnExplicitTool(t *testing.T) {
	stub := stubToolResolution(t, []string{"claude", "codex"}, true, "codex")
	got, err := resolveTool("pi", true)
	if err != nil || got != "pi" {
		t.Fatalf("resolveTool = %q, %v, want pi", got, err)
	}
	if len(stub.Offered) != 0 || len(stub.Saved) != 0 {
		t.Fatalf("an explicit tool must neither ask nor save, asked %v saved %v", stub.Offered, stub.Saved)
	}
}

// Scripts, CI, --json and --yes cannot be asked, and an unset tool is an error
// there rather than a guess. The message names the ways to configure one.
func TestResolveToolFailsWhenItCannotAsk(t *testing.T) {
	for _, tc := range []struct {
		name     string
		allowed  bool
		terminal bool
		tools    []string
	}{
		{name: "prompt not allowed", allowed: false, terminal: true, tools: []string{"claude", "codex"}},
		{name: "not a terminal", allowed: true, terminal: false, tools: []string{"claude", "codex"}},
		{name: "no installed agents", allowed: true, terminal: true, tools: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := stubToolResolution(t, tc.tools, tc.terminal, "codex")
			got, err := resolveTool(model.ToolAuto, tc.allowed)
			if err == nil || got != "" {
				t.Fatalf("resolveTool = %q, %v, want an error", got, err)
			}
			for _, hint := range []string{"--tool", `"tool"`} {
				if !strings.Contains(err.Error(), hint) {
					t.Errorf("error %q must mention %s", err, hint)
				}
			}
			if len(tc.tools) > 0 && !strings.Contains(err.Error(), "claude, codex") {
				t.Errorf("error %q must list the installed agents", err)
			}
			if len(stub.Offered) != 0 {
				t.Fatalf("no question may be asked, got %v", stub.Offered)
			}
			if len(stub.Saved) != 0 {
				t.Fatalf("nothing may be saved, got %v", stub.Saved)
			}
		})
	}
}

// The first interactive run asks once and persists the answer, so the second
// run reads it from the global config and never asks again.
func TestResolveToolAsksOnceAndSavesTheAnswer(t *testing.T) {
	stub := stubToolResolution(t, []string{"claude", "codex", "pi"}, true, "codex")
	got, err := resolveTool(model.ToolAuto, true)
	if err != nil || got != "codex" {
		t.Fatalf("resolveTool = %q, %v, want codex", got, err)
	}
	if len(stub.Offered) != 1 || stub.Offered[0] != "claude,codex,pi" {
		t.Fatalf("asked %v, want one question offering every installed agent", stub.Offered)
	}
	if len(stub.Saved) != 1 || stub.Saved[0] != "tool=codex" {
		t.Fatalf("saved %v, want tool=codex", stub.Saved)
	}
	// A saved choice reaches the next run as a configured value.
	stub.Offered, stub.Saved = nil, nil
	got, err = resolveTool("codex", true)
	if err != nil || got != "codex" {
		t.Fatalf("second run resolveTool = %q, %v, want codex", got, err)
	}
	if len(stub.Offered) != 0 {
		t.Fatalf("second run must not ask, got %v", stub.Offered)
	}
}

// An unanswered question (EOF, or answers the prompt cannot match) fails the
// run and persists nothing.
func TestResolveToolUnansweredFailsWithoutSaving(t *testing.T) {
	stub := stubToolResolution(t, []string{"claude", "codex"}, true, "")
	got, err := resolveTool(model.ToolAuto, true)
	if err == nil || got != "" {
		t.Fatalf("resolveTool = %q, %v, want an error", got, err)
	}
	if len(stub.Offered) != 1 {
		t.Fatalf("asked %v, want exactly one question", stub.Offered)
	}
	if len(stub.Saved) != 0 {
		t.Fatalf("an unanswered question must not save, got %v", stub.Saved)
	}
}

func TestActionNeedsTool(t *testing.T) {
	for _, tc := range []struct {
		name   string
		parsed cli.Result
		want   bool
	}{
		{name: "plain run", parsed: cli.Result{Action: "run"}, want: true},
		{name: "shell", parsed: cli.Result{Action: "shell"}, want: true},
		{name: "continue", parsed: cli.Result{Action: actionContinue}, want: true},
		{name: "exec", parsed: cli.Result{Action: "exec"}, want: true},
		{name: "update", parsed: cli.Result{Action: "update"}, want: true},
		// Explicit targets rebuild exactly those images; the default tool is
		// never read, so asking for one would save an unrelated answer.
		{name: "update with explicit targets", parsed: cli.Result{Action: "update", Options: model.Options{UpdateOptions: model.UpdateOptions{UpdateTools: []string{"codex"}}}}, want: false},
		{name: "info", parsed: cli.Result{Action: "info"}, want: true},
		{name: "auth-import", parsed: cli.Result{Action: "auth-import"}, want: true},
		{name: "devcontainer-generate", parsed: cli.Result{Action: "devcontainer-generate"}, want: true},
		{name: "network-print", parsed: cli.Result{Action: "network-print"}, want: true},
		{name: "network-log", parsed: cli.Result{Action: "network-log"}, want: true},
		// Background sessions of the default tool are stopped; a named
		// session is found without it.
		{name: "stop", parsed: cli.Result{Action: "stop"}, want: true},
		{name: "stop named session", parsed: cli.Result{Action: "stop", Options: model.Options{RunOptions: model.RunOptions{CmdArgs: []string{"my-task"}}}}, want: false},
		{name: "cleanup", parsed: cli.Result{Action: "cleanup"}, want: true},
		{name: "cleanup --all", parsed: cli.Result{Action: "cleanup", Options: model.Options{CleanupOptions: model.CleanupOptions{CleanupAll: true}}}, want: false},
		{name: "ps", parsed: cli.Result{Action: "ps"}, want: false},
		{name: "status", parsed: cli.Result{Action: "status"}, want: false},
		{name: "attach", parsed: cli.Result{Action: "attach"}, want: false},
		{name: "tools", parsed: cli.Result{Action: "tools"}, want: false},
		{name: "features", parsed: cli.Result{Action: "features"}, want: false},
		{name: "extension-manage", parsed: cli.Result{Action: cli.ActionExtensionManage}, want: false},
		{name: "config", parsed: cli.Result{Action: "config"}, want: false},
		{name: "review-target", parsed: cli.Result{Action: "review-target"}, want: false},
		{name: "theia", parsed: cli.Result{Action: "theia"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := actionNeedsTool(tc.parsed); got != tc.want {
				t.Fatalf("actionNeedsTool(%s) = %v, want %v", tc.parsed.Action, got, tc.want)
			}
		})
	}
}

// --json and --yes runs cannot be asked; the unset tool then fails instead.
func TestToolPromptNotAllowedForJSONAndYes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		parsed cli.Result
	}{
		{name: "--json extension request", parsed: cli.Result{Action: "run", ExtRequest: &extinstall.Request{JSON: true}}},
		{name: "--yes", parsed: cli.Result{Action: "run", ExtRequest: &extinstall.Request{Yes: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if promptAllowed(tc.parsed) {
				t.Fatalf("promptAllowed(%s) = true, want false", tc.name)
			}
		})
	}
}

func TestToolUnset(t *testing.T) {
	for _, tc := range []struct {
		tool string
		want bool
	}{
		{tool: model.ToolAuto, want: true},
		{tool: "  auto ", want: true},
		{tool: "", want: true},
		{tool: "claude", want: false},
	} {
		if got := toolUnset(tc.tool); got != tc.want {
			t.Fatalf("toolUnset(%q) = %v, want %v", tc.tool, got, tc.want)
		}
	}
}

// The default tool must stay unset so the first run can ask; a hardcoded
// vendor default is exactly what the question replaces.
func TestDefaultToolIsUnset(t *testing.T) {
	if got := config.DefaultOptions().Tool; got != model.ToolAuto {
		t.Fatalf("default tool = %q, want %q", got, model.ToolAuto)
	}
}

// The IDE profiles attach a host IDE to a container instead of running an
// agent in the terminal, so the question must not offer them.
func TestAgentToolNamesSkipsIDEProfiles(t *testing.T) {
	profiles := []model.Profile{
		{Name: "claude"},
		{Name: "theia", PostStart: &model.PostStartActions{OpenIDE: "theia"}},
		{Name: "theia-next", PostStart: &model.PostStartActions{OpenIDE: "theia-next"}},
		{Name: "pi"},
	}
	if got := strings.Join(agentToolNames(profiles), ","); got != "claude,pi" {
		t.Fatalf("names = %q, want the agent profiles only", got)
	}
}

// The bundled tool tree must produce a usable menu: the agent profiles, and
// none of the IDE ones. The exact list is deliberately not asserted, so adding
// or removing a tool extension does not fail this test.
func TestHostAgentToolsOffersTheBundledAgents(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "extensions", "tools")); err != nil {
		t.Skipf("bundled tool extensions not found: %v", err)
	}
	t.Setenv(model.EnvHome, root)
	t.Setenv("HOME", t.TempDir())

	offered := map[string]bool{}
	for _, name := range hostAgentTools() {
		offered[name] = true
	}
	for _, name := range []string{"claude", "codex"} {
		if !offered[name] {
			t.Errorf("agent profile %s must be offered, got %v", name, offered)
		}
	}
	for _, name := range []string{"theia", "theia-next"} {
		if offered[name] {
			t.Errorf("IDE profile %s must not be offered, got %v", name, offered)
		}
	}
}
