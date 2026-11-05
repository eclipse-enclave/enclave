// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enclave/internal/a2aagent"
	"enclave/internal/tools"
)

const fixedSessionID = "11111111-2222-4333-8444-555555555555"

func testSpec() tools.A2ASpec {
	return tools.A2ASpec{
		Command:        []string{"claude", "-p", "--output-format", "stream-json", "--verbose"},
		NewSessionArgs: []string{"--session-id", tools.SessionIDPlaceholder},
		ResumeArgs:     []string{"--resume", tools.SessionIDPlaceholder},
		EventFormat:    tools.EventFormatClaudeStreamJSON,
	}
}

func newTestDriver(t *testing.T, spec tools.A2ASpec, opts Options) *Driver {
	t.Helper()
	if opts.NewSessionID == nil {
		opts.NewSessionID = func() (string, error) { return fixedSessionID, nil }
	}
	d, err := New(spec, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func TestNewRejectsInvalidSpec(t *testing.T) {
	spec := testSpec()
	spec.ResumeArgs = []string{"--continue"}
	if _, err := New(spec, Options{}); err == nil || !strings.Contains(err.Error(), "a2a.resumeArgs") {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandNewContext(t *testing.T) {
	d := newTestDriver(t, testSpec(), Options{ExtraArgs: []string{"--dangerously-skip-permissions"}})
	cmd, err := d.Command(context.Background(), a2aagent.Invocation{
		Prompt:              "do not put me in argv",
		NewHarnessSessionID: fixedSessionID,
		WorkDir:             "/workspace/project",
		Env:                 []string{"HOME=/home/user", "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--session-id", fixedSessionID}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("args = %q, want %q", cmd.Args, want)
	}
	if cmd.Dir != "/workspace/project" {
		t.Fatalf("dir = %q", cmd.Dir)
	}
	if last := cmd.Env[len(cmd.Env)-1]; last != DisableBackgroundTasksEnv {
		t.Fatalf("last env = %q, want %q to win", last, DisableBackgroundTasksEnv)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "argv") {
		t.Fatal("prompt leaked into argv")
	}
}

func TestCommandNewContextRequiresBoundSessionID(t *testing.T) {
	d := newTestDriver(t, testSpec(), Options{})
	for _, bad := range []string{"", "-x", "../x"} {
		if _, err := d.Command(context.Background(), a2aagent.Invocation{NewHarnessSessionID: bad}); err == nil {
			t.Errorf("new session ID %q accepted", bad)
		}
	}
}

func TestNewSessionIDIsRandomUUID(t *testing.T) {
	d, err := New(testSpec(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	idA, err := d.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	idB, err := d.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if !uuidPattern.MatchString(idA) || idA == idB || idA[14] != '4' {
		t.Fatalf("session IDs %q, %q", idA, idB)
	}
}

func TestCommandResume(t *testing.T) {
	d := newTestDriver(t, testSpec(), Options{})
	cmd, err := d.Command(context.Background(), a2aagent.Invocation{HarnessSessionID: "f01fbe4e-97ea-48af-9b94-7ad99192c754"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cmd.Args[len(cmd.Args)-2:], " "); got != "--resume f01fbe4e-97ea-48af-9b94-7ad99192c754" {
		t.Fatalf("resume args = %q", got)
	}
	for _, bad := range []string{"--dangerously-skip-permissions", "../x", "f01fbe4e"} {
		if _, err := d.Command(context.Background(), a2aagent.Invocation{HarnessSessionID: bad}); err == nil {
			t.Errorf("session ID %q accepted", bad)
		}
	}
}

func TestCommandWithoutNewSessionArgs(t *testing.T) {
	spec := testSpec()
	spec.NewSessionArgs = nil
	d := newTestDriver(t, spec, Options{})
	if id, err := d.NewSessionID(); err != nil || id != "" {
		t.Fatalf("NewSessionID = %q, %v; want empty", id, err)
	}
	cmd, err := d.Command(context.Background(), a2aagent.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cmd.Args, spec.Command) {
		t.Fatalf("args = %q", cmd.Args)
	}
	if cmd.Env == nil {
		t.Fatal("env must be set so the background-task switch applies")
	}
}

func TestNewSessionIDRejectsBadGeneratedID(t *testing.T) {
	d := newTestDriver(t, testSpec(), Options{NewSessionID: func() (string, error) { return "-x", nil }})
	if _, err := d.NewSessionID(); err == nil {
		t.Fatal("expected an error")
	}
	d = newTestDriver(t, testSpec(), Options{NewSessionID: func() (string, error) { return "", errors.New("no entropy") }})
	if _, err := d.NewSessionID(); err == nil || !strings.Contains(err.Error(), "no entropy") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckVersion(t *testing.T) {
	script := filepath.Join(t.TempDir(), "version")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '2.1.306 (Claude Code)'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		command  []string
		versions string
		want     string
	}{
		"in range":      {[]string{script}, ">=2.1.306 <3.0.0", ""},
		"too old":       {[]string{script}, ">=2.1.307", "version 2.1.306 is outside the supported range >=2.1.307"},
		"too new":       {[]string{script}, "<2.0.0", "outside the supported range"},
		"command fails": {[]string{filepath.Join(t.TempDir(), "missing")}, ">=1.0.0", "missing"},
		"no version":    {[]string{"true"}, ">=1.0.0", "no major.minor.patch version"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spec := testSpec()
			spec.VersionCommand, spec.Versions = tc.command, tc.versions
			err := newTestDriver(t, spec, Options{}).CheckVersion(context.Background())
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if err := newTestDriver(t, testSpec(), Options{}).CheckVersion(context.Background()); err != nil {
		t.Fatalf("no version command: %v", err)
	}
}

func TestProjectDirName(t *testing.T) {
	for in, want := range map[string]string{
		"/tmp/a2a-p2/work":                    "-tmp-a2a-p2-work",
		"/home/u/.local/share/x_y":            "-home-u--local-share-x-y",
		"/w/café":                             "-w-caf-",
		"/w/\U0001F600":                       "-w---",
		"":                                    "",
		"/" + strings.Repeat("a", 200):        "",
		"/" + strings.Repeat("a", 199):        "-" + strings.Repeat("a", 199),
		"/workspace/enclave/.claude/worktree": "-workspace-enclave--claude-worktree",
	} {
		if got := projectDirName(in); got != want {
			t.Errorf("projectDirName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTranscriptPaths(t *testing.T) {
	got := TranscriptPaths("/tmp/w", "abc")
	want := []string{"projects/-tmp-w/abc.jsonl", "projects/-tmp-w/abc", "session-env/abc", "file-history/abc", "tasks/abc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if got := TranscriptPaths("", "abc"); len(got) != 3 {
		t.Fatalf("without cwd: %q", got)
	}
}
