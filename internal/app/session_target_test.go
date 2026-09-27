// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
	"enclave/internal/runtime"
)

func session(name string, sessionName string, hash string, dir string) backend.Session {
	return backend.Session{
		Ref:         backend.SessionRef{Name: name},
		Tool:        "claude",
		ProjectHash: hash,
		Worktree:    dir,
		ProjectDir:  dir,
		Name:        sessionName,
		Status:      "running",
	}
}

func TestResolveSessionTargetAcceptsFullContainerName(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
	}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Args: []string{"enclave-claude-aaaaaaaaaaaa-my-task"},
	})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-claude-aaaaaaaaaaaa-my-task" {
		t.Fatalf("resolved %q, want the container name it was given", got.Ref.Name)
	}
}

func TestResolveSessionTargetResolvesSessionName(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
	}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Args: []string{"my-task"}})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-claude-aaaaaaaaaaaa-my-task" {
		t.Fatalf("resolved %q, want the container of session my-task", got.Ref.Name)
	}
}

func TestResolveSessionTargetSanitizesRequestedName(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
	}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Args: []string{"My Task"}})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-claude-aaaaaaaaaaaa-my-task" {
		t.Fatalf("resolved %q, want the sanitized name to match", got.Ref.Name)
	}
}

func TestResolveSessionTargetPrefersCurrentProject(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
		session("enclave-claude-bbbbbbbbbbbb-my-task", "my-task", "bbbbbbbbbbbb", "/repo/b"),
	}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Args:    []string{"my-task"},
		Project: model.Project{Hash: "bbbbbbbbbbbb", Dir: "/repo/b", RealDir: "/repo/b"},
	})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-claude-bbbbbbbbbbbb-my-task" {
		t.Fatalf("resolved %q, want the session of the current project", got.Ref.Name)
	}
}

func TestResolveSessionTargetResolvesAcrossProjectsWhenUnambiguous(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
	}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Args:    []string{"my-task"},
		Project: model.Project{Hash: "bbbbbbbbbbbb", Dir: "/repo/b", RealDir: "/repo/b"},
	})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-claude-aaaaaaaaaaaa-my-task" {
		t.Fatalf("resolved %q, want the only session carrying the name", got.Ref.Name)
	}
}

func TestResolveSessionTargetReportsAmbiguousSessionName(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
		session("enclave-claude-bbbbbbbbbbbb-my-task", "my-task", "bbbbbbbbbbbb", "/repo/b"),
	}}

	_, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Args: []string{"my-task"}})
	if err == nil {
		t.Fatal("resolveSessionTarget() must not guess between projects")
	}
	for _, want := range []string{"enclave-claude-aaaaaaaaaaaa-my-task", "enclave-claude-bbbbbbbbbbbb-my-task"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not list candidate %q", err, want)
		}
	}
}

func TestResolveSessionTargetReportsUnknownName(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
	}}

	_, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Args: []string{"other"}})
	if err == nil || !strings.Contains(err.Error(), `"other"`) {
		t.Fatalf("error = %v, want it to name the unknown session", err)
	}
}

func TestResolveSessionTargetRejectsBlankArgument(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
	}}
	project := model.Project{Hash: "aaaaaaaaaaaa", Dir: "/repo/a", RealDir: "/repo/a"}

	// `enclave stop "$SESSION"` with an unset variable must not fall through to
	// auto-selection and remove the project's only session.
	for _, args := range [][]string{{""}, {"   "}} {
		if _, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
			Args:    args,
			Project: project,
		}); err == nil {
			t.Fatalf("resolveSessionTarget(%q) must reject a blank argument", args)
		}
	}
}

func TestResolveSessionTargetProjectScopedNamesStayInProject(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-1", "1", "aaaaaaaaaaaa", "/repo/a"),
	}}

	_, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Args:               []string{"1"},
		Project:            model.Project{Hash: "bbbbbbbbbbbb", Dir: "/repo/b", RealDir: "/repo/b"},
		ProjectScopedNames: true,
	})
	if err == nil {
		t.Fatal("a project-scoped name must not resolve to another project's session")
	}
	if !strings.Contains(err.Error(), "another project") {
		t.Fatalf("error %q does not point at the container-name escape hatch", err)
	}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Args:               []string{"1"},
		Project:            model.Project{Hash: "aaaaaaaaaaaa", Dir: "/repo/a", RealDir: "/repo/a"},
		ProjectScopedNames: true,
	})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-claude-aaaaaaaaaaaa-1" {
		t.Fatalf("resolved %q, want the session of the current project", got.Ref.Name)
	}
}

func TestResolveSessionTargetAutoSelectsWithinProject(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa", "", "aaaaaaaaaaaa", "/repo/a"),
		session("enclave-claude-bbbbbbbbbbbb-my-task", "my-task", "bbbbbbbbbbbb", "/repo/b"),
	}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Project: model.Project{Hash: "aaaaaaaaaaaa", Dir: "/repo/a", RealDir: "/repo/a"},
	})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-claude-aaaaaaaaaaaa" {
		t.Fatalf("resolved %q, want the running session of the current project", got.Ref.Name)
	}
}

func TestResolveSessionTargetAutoSelectStaysInProject(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
	}}

	_, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Project: model.Project{Hash: "bbbbbbbbbbbb", Dir: "/repo/b", RealDir: "/repo/b"},
	})
	if err == nil {
		t.Fatal("auto-selection must not leave the current project")
	}
}

func TestResolveSessionTargetAutoSelectSkipsForegroundSessions(t *testing.T) {
	foreground := session("enclave-claude-aaaaaaaaaaaa", "", "aaaaaaaaaaaa", "/repo/a")
	detached := session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a")
	detached.Background = true
	be := &stopTestBackend{sessions: []backend.Session{foreground, detached}}
	project := model.Project{Hash: "aaaaaaaaaaaa", Dir: "/repo/a", RealDir: "/repo/a"}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Project:        project,
		BackgroundOnly: true,
	})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != detached.Ref.Name {
		t.Fatalf("resolved %q, want the detached session", got.Ref.Name)
	}

	be.sessions = []backend.Session{foreground}
	if _, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Project:        project,
		BackgroundOnly: true,
	}); err == nil {
		t.Fatal("a foreground session must not be auto-selected")
	}
}

func TestResolveSessionTargetResolvesContainerID(t *testing.T) {
	target := session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a")
	target.Ref.ID = "3f2b1c0d4e5f"
	be := &stopTestBackend{sessions: []backend.Session{target}}

	fullID := target.Ref.ID + strings.Repeat("a", containerIDFullLen-len(target.Ref.ID))
	for _, requested := range []string{"3f2b1c0d4e5f", "3f2b1c0d", fullID} {
		got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Args: []string{requested}})
		if err != nil {
			t.Fatalf("resolveSessionTarget(%q) error = %v", requested, err)
		}
		if got.Ref.Name != target.Ref.Name {
			t.Fatalf("resolveSessionTarget(%q) = %q, want the container with that ID", requested, got.Ref.Name)
		}
	}
}

func TestResolveSessionTargetReportsAmbiguousContainerIDPrefix(t *testing.T) {
	first := session("enclave-claude-aaaaaaaaaaaa-one", "one", "aaaaaaaaaaaa", "/repo/a")
	first.Ref.ID = "3f2b1c0d4e5f"
	second := session("enclave-claude-bbbbbbbbbbbb-two", "two", "bbbbbbbbbbbb", "/repo/b")
	second.Ref.ID = "3f2b1c0daaaa"
	be := &stopTestBackend{sessions: []backend.Session{first, second}}

	_, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Args: []string{"3f2b1c0d"}})
	if err == nil {
		t.Fatal("an ambiguous ID prefix must not be resolved by listing order")
	}
	for _, want := range []string{first.Ref.Name, second.Ref.Name} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not list candidate %q", err, want)
		}
	}
}

func TestResolveSessionTargetRejectsPartialContainerIDExtension(t *testing.T) {
	target := session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a")
	target.Ref.ID = "3f2b1c0d4e5f"
	be := &stopTestBackend{sessions: []backend.Session{target}}

	// Only a complete ID may extend the recorded (truncated) one; a value of
	// some length in between cannot be verified against it.
	for _, requested := range []string{target.Ref.ID + "aaaaaaaa", target.Ref.ID + strings.Repeat("a", containerIDFullLen)} {
		if _, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Args: []string{requested}}); err == nil {
			t.Fatalf("resolveSessionTarget(%q) must not match on the recorded ID prefix", requested)
		}
	}
}

func TestResolveSessionTargetPrefersSessionNameOverContainerID(t *testing.T) {
	named := session("enclave-claude-aaaaaaaaaaaa-deadbeef", "deadbeef", "aaaaaaaaaaaa", "/repo/a")
	other := session("enclave-claude-bbbbbbbbbbbb-other", "other", "bbbbbbbbbbbb", "/repo/b")
	other.Ref.ID = "deadbeef1234"
	be := &stopTestBackend{sessions: []backend.Session{named, other}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Args: []string{"deadbeef"}})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != named.Ref.Name {
		t.Fatalf("resolved %q, want the session name to win over a container ID", got.Ref.Name)
	}
}

func TestResolveSessionTargetToolFilterKeepsExactContainerName(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-codex-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
	}}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Args: []string{"enclave-codex-aaaaaaaaaaaa-my-task"},
		Tool: "claude",
	})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-codex-aaaaaaaaaaaa-my-task" {
		t.Fatalf("resolved %q, want an exact container name to ignore the tool filter", got.Ref.Name)
	}
	if be.listFilter.Tool != "" {
		t.Fatalf("listFilter.Tool = %q, want the tool applied to candidates, not the listing", be.listFilter.Tool)
	}
}

func TestResolveSessionTargetToolNarrowsAmbiguousSessionName(t *testing.T) {
	be := &stopTestBackend{sessions: []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
		{
			Ref:         backend.SessionRef{Name: "enclave-codex-aaaaaaaaaaaa-my-task"},
			Tool:        "codex",
			ProjectHash: "aaaaaaaaaaaa",
			Worktree:    "/repo/a",
			ProjectDir:  "/repo/a",
			Name:        "my-task",
			Status:      "running",
		},
	}}
	project := model.Project{Hash: "aaaaaaaaaaaa", Dir: "/repo/a", RealDir: "/repo/a"}

	if _, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Args:    []string{"my-task"},
		Project: project,
	}); err == nil {
		t.Fatal("one name used by two tools in a project must be reported as ambiguous")
	}

	got, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{
		Args:    []string{"my-task"},
		Tool:    "codex",
		Project: project,
	})
	if err != nil {
		t.Fatalf("resolveSessionTarget() error = %v", err)
	}
	if got.Ref.Name != "enclave-codex-aaaaaaaaaaaa-my-task" {
		t.Fatalf("resolved %q, want --tool to pick the codex session", got.Ref.Name)
	}
}

func TestSessionsMatchingNameSanitizesBothSides(t *testing.T) {
	sessions := []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "My Task", "aaaaaaaaaaaa", "/repo/a"),
		session("enclave-claude-aaaaaaaaaaaa-other", "other", "aaaaaaaaaaaa", "/repo/a"),
	}

	for _, requested := range []string{"My Task", "my-task"} {
		matches := sessionsMatchingName(sessions, requested)
		if len(matches) != 1 || matches[0].Name != "My Task" {
			t.Fatalf("sessionsMatchingName(%q) = %v, want the session labeled with the raw name", requested, matches)
		}
	}
}

func TestSessionsMatchingNameIgnoresUnsanitizableName(t *testing.T) {
	sessions := []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa-my-task", "my-task", "aaaaaaaaaaaa", "/repo/a"),
		session("enclave-claude-aaaaaaaaaaaa", "", "aaaaaaaaaaaa", "/repo/a"),
	}

	// A name that sanitizes to nothing must match nothing: `stop --name "???"`
	// must not turn into "stop everything".
	if matches := sessionsMatchingName(sessions, "???"); len(matches) != 0 {
		t.Fatalf("sessionsMatchingName(\"???\") = %v, want no matches", matches)
	}
}

func TestResolveSessionTargetListsOnlyRunningByDefault(t *testing.T) {
	be := &stopTestBackend{}

	if _, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Args: []string{"my-task"}}); err == nil {
		t.Fatal("expected an error for an empty session list")
	}
	if !be.listFilter.RunningOnly || be.listFilter.All {
		t.Fatalf("filter = %+v, want running sessions only", be.listFilter)
	}

	if _, err := resolveSessionTarget(context.Background(), be, sessionTargetQuery{Args: []string{"my-task"}, IncludeStopped: true}); err == nil {
		t.Fatal("expected an error for an empty session list")
	}
	if !be.listFilter.All || be.listFilter.RunningOnly {
		t.Fatalf("filter = %+v, want stopped sessions included", be.listFilter)
	}
}

// execOptions returns the options of `enclave exec --name <name>`, optionally
// with an explicit `--tool`.
func execOptions(name string, tool string) model.Options {
	var opts model.Options
	opts.Tool = "claude"
	opts.SessionName = name
	opts.Sources.SessionName = model.SourceCLI
	if tool != "" {
		opts.Tool = tool
		opts.Sources.Tool = model.SourceCLI
	}
	return opts
}

func execTestSessions() []backend.Session {
	codexReview := session("enclave-codex-bbbbbbbbbbbb-review", "review", "bbbbbbbbbbbb", "/repo/b")
	codexReview.Tool = "codex"
	codexReview.Ref.ID = "0123456789abcdef"
	codexOne := session("enclave-codex-bbbbbbbbbbbb-1", "1", "bbbbbbbbbbbb", "/repo/b")
	codexOne.Tool = "codex"
	return []backend.Session{
		session("enclave-claude-aaaaaaaaaaaa", "", "aaaaaaaaaaaa", "/repo/a"),
		session("enclave-claude-aaaaaaaaaaaa-1", "1", "aaaaaaaaaaaa", "/repo/a"),
		session("enclave-claude-bbbbbbbbbbbb-1", "1", "bbbbbbbbbbbb", "/repo/b"),
		codexReview,
		codexOne,
	}
}

var (
	execProjectA    = model.Project{Hash: "aaaaaaaaaaaa", Dir: "/repo/a", RealDir: "/repo/a"}
	execProjectHome = model.Project{Hash: "cccccccccccc", Dir: "/home/user", RealDir: "/home/user"}
)

func TestExecSessionTargetAcceptsContainerNameFromAnyDirectory(t *testing.T) {
	for _, name := range []string{
		"enclave-claude-aaaaaaaaaaaa",       // the project's default container (SESSION "-")
		"enclave-claude-aaaaaaaaaaaa-1",     // a named session
		"enclave-codex-bbbbbbbbbbbb-review", // another tool, without --tool
	} {
		be := &stopTestBackend{sessions: execTestSessions()}
		got, err := execSessionTarget(context.Background(), be, execOptions(name, ""), execProjectHome)
		if err != nil {
			t.Fatalf("execSessionTarget(%q) error = %v", name, err)
		}
		if got.Name != name {
			t.Fatalf("execSessionTarget(%q) = %q, want the container name verbatim", name, got.Name)
		}
	}
}

func TestExecSessionTargetAcceptsContainerID(t *testing.T) {
	be := &stopTestBackend{sessions: execTestSessions()}

	got, err := execSessionTarget(context.Background(), be, execOptions("0123456789ab", ""), execProjectHome)
	if err != nil {
		t.Fatalf("execSessionTarget() error = %v", err)
	}
	if got.Name != "enclave-codex-bbbbbbbbbbbb-review" {
		t.Fatalf("execSessionTarget() = %q, want the container with that ID", got.Name)
	}
}

func TestExecSessionTargetPrefersCurrentProjectSessionName(t *testing.T) {
	be := &stopTestBackend{sessions: execTestSessions()}

	got, err := execSessionTarget(context.Background(), be, execOptions("1", ""), execProjectA)
	if err != nil {
		t.Fatalf("execSessionTarget() error = %v", err)
	}
	if got.Name != "enclave-claude-aaaaaaaaaaaa-1" {
		t.Fatalf("execSessionTarget() = %q, want the session of the current project", got.Name)
	}
}

func TestExecSessionTargetResolvesSessionNameOfOtherProject(t *testing.T) {
	be := &stopTestBackend{sessions: execTestSessions()}

	got, err := execSessionTarget(context.Background(), be, execOptions("review", ""), execProjectHome)
	if err != nil {
		t.Fatalf("execSessionTarget() error = %v", err)
	}
	if got.Name != "enclave-codex-bbbbbbbbbbbb-review" {
		t.Fatalf("execSessionTarget() = %q, want the only session with that name", got.Name)
	}
}

func TestExecSessionTargetReportsAmbiguousSessionName(t *testing.T) {
	be := &stopTestBackend{sessions: execTestSessions()}

	_, err := execSessionTarget(context.Background(), be, execOptions("1", ""), execProjectHome)
	if err == nil {
		t.Fatal("execSessionTarget() succeeded, want an ambiguity error")
	}
	for _, want := range []string{"enclave-claude-aaaaaaaaaaaa-1", "enclave-claude-bbbbbbbbbbbb-1", "enclave-codex-bbbbbbbbbbbb-1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to list %s", err, want)
		}
	}
}

func TestExecSessionTargetToolNarrowsSessionNameOnlyWhenExplicit(t *testing.T) {
	be := &stopTestBackend{sessions: execTestSessions()}

	got, err := execSessionTarget(context.Background(), be, execOptions("1", "codex"), execProjectHome)
	if err != nil {
		t.Fatalf("execSessionTarget() error = %v", err)
	}
	if got.Name != "enclave-codex-bbbbbbbbbbbb-1" {
		t.Fatalf("execSessionTarget() = %q, want the codex session", got.Name)
	}

	// The configured default tool is not a filter: a codex session is reachable
	// while claude is the default.
	got, err = execSessionTarget(context.Background(), be, execOptions("review", ""), execProjectA)
	if err != nil {
		t.Fatalf("execSessionTarget() error = %v", err)
	}
	if got.Name != "enclave-codex-bbbbbbbbbbbb-review" {
		t.Fatalf("execSessionTarget() = %q, want the codex session", got.Name)
	}
}

func TestExecSessionTargetReportsUnknownName(t *testing.T) {
	be := &stopTestBackend{sessions: execTestSessions()}

	_, err := execSessionTarget(context.Background(), be, execOptions("nope", ""), execProjectA)
	if err == nil {
		t.Fatal("execSessionTarget() succeeded, want an error")
	}
	if !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "enclave ps") {
		t.Fatalf("error = %q, want the name and a pointer to `enclave ps`", err)
	}
}

func TestExecSessionTargetRejectsBlankName(t *testing.T) {
	for _, name := range []string{"", "  "} {
		be := &stopTestBackend{sessions: execTestSessions()}
		if _, err := execSessionTarget(context.Background(), be, execOptions(name, ""), execProjectA); err == nil {
			t.Fatalf("execSessionTarget(%q) succeeded, want an error instead of auto-selecting", name)
		}
	}
}

// The ambient tool is claude, so exec into a codex session paints the codex
// color, as attach does.
func TestExecSessionTargetFollowsSessionTint(t *testing.T) {
	writeGlobalToolTints(t)
	codex := session("enclave-codex-bbbbbbbbbbbb-review", "review", "bbbbbbbbbbbb", t.TempDir())
	codex.Tool = "codex"
	be := &stopTestBackend{sessions: []backend.Session{codex}}
	opts := execOptions("review", "")
	opts.SessionTint = "#111111"

	got, err := execSessionTarget(context.Background(), be, opts, execProjectHome)
	if err != nil {
		t.Fatalf("execSessionTarget() error = %v", err)
	}
	if got.SessionTint != "#222222" {
		t.Fatalf("session tint = %q, want the codex tint", got.SessionTint)
	}
}

func TestExecSessionTargetReachesForegroundRunningSessions(t *testing.T) {
	foreground := session("enclave-claude-aaaaaaaaaaaa-work", "work", "aaaaaaaaaaaa", "/repo/a")
	foreground.Background = false
	be := &stopTestBackend{sessions: []backend.Session{foreground}}

	got, err := execSessionTarget(context.Background(), be, execOptions("work", ""), execProjectA)
	if err != nil {
		t.Fatalf("execSessionTarget() error = %v", err)
	}
	if got.Name != foreground.Ref.Name {
		t.Fatalf("execSessionTarget() = %q, want the foreground session", got.Name)
	}
	if !be.listFilter.RunningOnly || be.listFilter.All {
		t.Fatalf("filter = %+v, want running sessions only", be.listFilter)
	}
}

func TestExecSessionTargetWithoutNameLeavesSelectionToRuntime(t *testing.T) {
	be := &stopTestBackend{sessions: execTestSessions()}
	var opts model.Options
	opts.Tool = "claude"

	got, err := execSessionTarget(context.Background(), be, opts, execProjectA)
	if err != nil {
		t.Fatalf("execSessionTarget() error = %v", err)
	}
	if got != (runtime.ExecTarget{}) {
		t.Fatalf("execSessionTarget() = %+v, want no target so exec auto-selects", got)
	}
}
