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
	if got := resolveTool("pi", true); got != "pi" {
		t.Fatalf("resolveTool = %q, want pi", got)
	}
	if len(stub.Offered) != 0 || len(stub.Saved) != 0 {
		t.Fatalf("an explicit tool must neither ask nor save, asked %v saved %v", stub.Offered, stub.Saved)
	}
}

// Scripts, CI, --json and --yes keep the historical default without a question.
func TestResolveToolNonInteractiveUsesClaude(t *testing.T) {
	for _, tc := range []struct {
		name     string
		allowed  bool
		terminal bool
	}{
		{name: "prompt not allowed", allowed: false, terminal: true},
		{name: "not a terminal", allowed: true, terminal: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := stubToolResolution(t, []string{"claude", "codex"}, tc.terminal, "codex")
			if got := resolveTool(model.ToolAuto, tc.allowed); got != toolFallback {
				t.Fatalf("resolveTool = %q, want %q", got, toolFallback)
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

// A host with exactly one installed agent has no question to ask, so the
// scripted path must reach the same tool the interactive one would rather than
// a claude that may not be installed.
func TestResolveToolNonInteractiveUsesTheOnlyInstalledAgent(t *testing.T) {
	stub := stubToolResolution(t, []string{"codex"}, false, "")
	if got := resolveTool(model.ToolAuto, false); got != "codex" {
		t.Fatalf("resolveTool = %q, want codex", got)
	}
	if len(stub.Offered) != 0 || len(stub.Saved) != 0 {
		t.Fatalf("a non-interactive run must neither ask nor save, asked %v saved %v", stub.Offered, stub.Saved)
	}
}

// The first interactive run asks once and persists the answer, so the second
// run reads it from the global config and never asks again.
func TestResolveToolAsksOnceAndSavesTheAnswer(t *testing.T) {
	stub := stubToolResolution(t, []string{"claude", "codex", "pi"}, true, "codex")
	if got := resolveTool(model.ToolAuto, true); got != "codex" {
		t.Fatalf("resolveTool = %q, want codex", got)
	}
	if len(stub.Offered) != 1 || stub.Offered[0] != "claude,codex,pi" {
		t.Fatalf("asked %v, want one question offering every installed agent", stub.Offered)
	}
	if len(stub.Saved) != 1 || stub.Saved[0] != "tool=codex" {
		t.Fatalf("saved %v, want tool=codex", stub.Saved)
	}
	// A saved choice reaches the next run as a configured value.
	stub.Offered, stub.Saved = nil, nil
	if got := resolveTool("codex", true); got != "codex" {
		t.Fatalf("second run resolveTool = %q, want codex", got)
	}
	if len(stub.Offered) != 0 {
		t.Fatalf("second run must not ask, got %v", stub.Offered)
	}
}

func TestResolveToolSkipsPointlessQuestions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tools []string
		want  string
	}{
		{name: "single tool", tools: []string{"codex"}, want: "codex"},
		{name: "no tools", tools: nil, want: toolFallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := stubToolResolution(t, tc.tools, true, "")
			if got := resolveTool(model.ToolAuto, true); got != tc.want {
				t.Fatalf("resolveTool = %q, want %q", got, tc.want)
			}
			if len(stub.Offered) != 0 {
				t.Fatalf("nothing to choose between, got question %v", stub.Offered)
			}
			if len(stub.Saved) != 0 {
				t.Fatalf("nothing may be saved, got %v", stub.Saved)
			}
		})
	}
}

// An unanswered question (EOF, or answers the prompt cannot match) must not
// block the run or persist anything.
func TestResolveToolUnansweredFallsBackWithoutSaving(t *testing.T) {
	stub := stubToolResolution(t, []string{"claude", "codex"}, true, "")
	if got := resolveTool(model.ToolAuto, true); got != toolFallback {
		t.Fatalf("resolveTool = %q, want %q", got, toolFallback)
	}
	if len(stub.Offered) != 1 {
		t.Fatalf("asked %v, want exactly one question", stub.Offered)
	}
	if len(stub.Saved) != 0 {
		t.Fatalf("an unanswered question must not save, got %v", stub.Saved)
	}
}

func TestToolPromptAllowed(t *testing.T) {
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
		{name: "ps", parsed: cli.Result{Action: "ps"}, want: false},
		{name: "status", parsed: cli.Result{Action: "status"}, want: false},
		{name: "stop", parsed: cli.Result{Action: "stop"}, want: false},
		{name: "tools", parsed: cli.Result{Action: "tools"}, want: false},
		{name: "features", parsed: cli.Result{Action: "features"}, want: false},
		{name: "config", parsed: cli.Result{Action: "config"}, want: false},
		{name: "review-target", parsed: cli.Result{Action: "review-target"}, want: false},
		{name: "theia", parsed: cli.Result{Action: "theia"}, want: false},
		{name: "run --json extension request", parsed: cli.Result{Action: "run", ExtRequest: &extinstall.Request{JSON: true}}, want: false},
		{name: "run --yes", parsed: cli.Result{Action: "run", ExtRequest: &extinstall.Request{Yes: true}}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolPromptAllowed(tc.parsed); got != tc.want {
				t.Fatalf("toolPromptAllowed(%s) = %v, want %v", tc.parsed.Action, got, tc.want)
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
