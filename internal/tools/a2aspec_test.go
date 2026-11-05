// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package tools

import (
	"strings"
	"testing"

	"enclave/internal/model"
)

func validA2ASpec() *A2ASpec {
	return &A2ASpec{
		Command:        []string{"claude", "-p", "--output-format", "stream-json", "--verbose"},
		NewSessionArgs: []string{"--session-id", SessionIDPlaceholder},
		ResumeArgs:     []string{"--resume", SessionIDPlaceholder},
		EventFormat:    EventFormatClaudeStreamJSON,
		VersionCommand: []string{"claude", "--version"},
		Versions:       ">=2.1.306 <3.0.0",
		Skills:         []A2ASkillSpec{{ID: "coding", Name: "Coding", Description: "Writes code."}},
	}
}

func TestValidateA2ASpecAcceptsValidBlock(t *testing.T) {
	if err := ValidateA2ASpec(validA2ASpec()); err != nil {
		t.Fatal(err)
	}
	if err := ValidateA2ASpec(nil); err != nil {
		t.Fatalf("nil block: %v", err)
	}
	minimal := &A2ASpec{Command: []string{"h"}, ResumeArgs: []string{"--resume={sessionId}"}, EventFormat: EventFormatClaudeStreamJSON}
	if err := ValidateA2ASpec(minimal); err != nil {
		t.Fatalf("minimal block: %v", err)
	}
}

func TestValidateA2ASpecRejects(t *testing.T) {
	cases := map[string]struct {
		mutate func(*A2ASpec)
		want   string
	}{
		"no command":                {func(s *A2ASpec) { s.Command = nil }, "a2a.command is required"},
		"blank command token":       {func(s *A2ASpec) { s.Command = []string{"claude", " "} }, "a2a.command[1] is blank"},
		"placeholder in command":    {func(s *A2ASpec) { s.Command = []string{"claude", "{sessionId}"} }, "a2a.command must not contain"},
		"no resume args":            {func(s *A2ASpec) { s.ResumeArgs = nil }, "a2a.resumeArgs is required"},
		"resume without id":         {func(s *A2ASpec) { s.ResumeArgs = []string{"--continue"} }, "a2a.resumeArgs must contain {sessionId} exactly once"},
		"resume with two ids":       {func(s *A2ASpec) { s.ResumeArgs = []string{"{sessionId}", "{sessionId}"} }, "exactly once"},
		"new session without id":    {func(s *A2ASpec) { s.NewSessionArgs = []string{"--new"} }, "a2a.newSessionArgs must contain"},
		"no event format":           {func(s *A2ASpec) { s.EventFormat = "" }, "a2a.eventFormat is required"},
		"unknown event format":      {func(s *A2ASpec) { s.EventFormat = "codex-jsonl" }, `a2a.eventFormat "codex-jsonl" is not supported`},
		"versions without command":  {func(s *A2ASpec) { s.VersionCommand = nil }, "must be set together"},
		"command without versions":  {func(s *A2ASpec) { s.Versions = "" }, "must be set together"},
		"bad range":                 {func(s *A2ASpec) { s.Versions = "2.1" }, "a2a.versions: constraint"},
		"skill without id":          {func(s *A2ASpec) { s.Skills[0].ID = "" }, "a2a.skills[0].id is required"},
		"skill without name":        {func(s *A2ASpec) { s.Skills[0].Name = "" }, "a2a.skills[0].name is required"},
		"skill without description": {func(s *A2ASpec) { s.Skills[0].Description = "" }, "a2a.skills[0].description is required"},
		"duplicate skill": {func(s *A2ASpec) {
			s.Skills = append(s.Skills, A2ASkillSpec{ID: "coding", Name: "n", Description: "d"})
		}, `a2a.skills[1].id "coding" is duplicated`},
		"blank tag":                {func(s *A2ASpec) { s.Skills[0].Tags = []string{""} }, "a2a.skills[0].tags[0] is blank"},
		"blank orchestrator arg":   {func(s *A2ASpec) { s.OrchestratorArgs = []string{"--mcp-config", ""} }, "a2a.orchestratorArgs[1] is blank"},
		"orchestrator arg with id": {func(s *A2ASpec) { s.OrchestratorArgs = []string{"{sessionId}"} }, "a2a.orchestratorArgs must not contain"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spec := validA2ASpec()
			tc.mutate(spec)
			err := ValidateA2ASpec(spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestVersionRange(t *testing.T) {
	r, err := ParseVersionRange(" >=2.1.306   <3.0.0 ")
	if err != nil {
		t.Fatal(err)
	}
	if r.String() != ">=2.1.306 <3.0.0" {
		t.Fatalf("String = %q", r.String())
	}
	for v, want := range map[string]bool{"2.1.305": false, "2.1.306": true, "2.9.0": true, "3.0.0": false, "10.0.0": false} {
		parsed, err := ParseVersion(v)
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Contains(parsed); got != want {
			t.Errorf("Contains(%s) = %v, want %v", v, got, want)
		}
	}
	exact, _ := ParseVersionRange("=1.2.3")
	if !exact.Contains(Version{Major: 1, Minor: 2, Patch: 3}) || exact.Contains(Version{Major: 1, Minor: 2, Patch: 4}) {
		t.Fatal("= constraint")
	}
	open, _ := ParseVersionRange(">1.0.0 <=1.0.5")
	if open.Contains(Version{Major: 1, Minor: 0, Patch: 0}) || !open.Contains(Version{Major: 1, Minor: 0, Patch: 5}) {
		t.Fatal("> and <= constraints")
	}
	for _, bad := range []string{"", "2.1.0", ">=2.1", "~2.1.0", ">=2.1.0-beta"} {
		if _, err := ParseVersionRange(bad); err == nil {
			t.Errorf("ParseVersionRange(%q) succeeded", bad)
		}
	}
}

func TestExtractVersion(t *testing.T) {
	v, err := ExtractVersion("2.1.306 (Claude Code)\n")
	if err != nil || v != (Version{Major: 2, Minor: 1, Patch: 306}) {
		t.Fatalf("v = %v, err = %v", v, err)
	}
	if _, err := ExtractVersion("unknown"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestRequireA2A(t *testing.T) {
	if _, err := RequireA2A(model.Profile{Name: "codex"}); err == nil || !strings.Contains(err.Error(), `tool "codex" cannot run in serve mode`) {
		t.Fatalf("err = %v", err)
	}
	spec := validA2ASpec()
	got, err := RequireA2A(model.Profile{Name: "claude", A2A: spec})
	if err != nil || got != spec {
		t.Fatalf("got %v, %v", got, err)
	}
}
