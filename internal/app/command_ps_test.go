// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
	"enclave/internal/docker"
	"enclave/internal/model"
)

func TestParseManagedContainerNameSupportsHyphenatedTools(t *testing.T) {
	tool, hash, session, ok := docker.ParseManagedName("enclave-open-code-abc123abc123-feature-1")
	if !ok {
		t.Fatal("expected managed container name to parse")
	}
	if tool != "open-code" {
		t.Fatalf("expected tool open-code, got %q", tool)
	}
	if hash != "abc123abc123" {
		t.Fatalf("expected hash abc123abc123, got %q", hash)
	}
	if session != "feature-1" {
		t.Fatalf("expected session feature-1, got %q", session)
	}
}

func TestBuildPSRowsFromSessions(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	rows := buildPSRowsFromSessions([]backend.Session{
		{
			Ref:         backend.SessionRef{Name: "enclave-codex-abc123abc123-main"},
			Tool:        "codex",
			ProjectHash: "abc123abc123",
			Worktree:    "/tmp/project-alpha",
			Status:      "running",
			CreatedAt:   now.Add(-90 * time.Minute),
			Name:        "main",
			Ports:       []backend.PortMapping{{HostIP: "127.0.0.1", HostPort: "3000", ContainerPort: "3000", Protocol: "tcp"}},
		},
		{
			Ref:         backend.SessionRef{Name: "enclave-claude-def456def456"},
			Tool:        "claude",
			ProjectHash: "def456def456",
			Status:      "running",
			CreatedAt:   now.Add(-30 * time.Second),
		},
		{
			Ref:         backend.SessionRef{Name: "enclave-opencode-aaa111aaa111"},
			Tool:        "opencode",
			ProjectHash: "aaa111aaa111",
			Status:      "exited",
			CreatedAt:   now.Add(-90 * time.Minute),
		},
	}, now, func(tool string) []model.PortConfig {
		if tool != "codex" {
			return nil
		}
		return []model.PortConfig{{Container: 3000, Publish: true, OpenURL: "http://localhost:{host_port}"}}
	})

	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	// The exited opencode row sorts last (by name) and shows a dash instead of a
	// growing age-based uptime.
	if rows[2].Name != "enclave-opencode-aaa111aaa111" {
		t.Fatalf("expected sorted opencode row last, got %q", rows[2].Name)
	}
	if rows[2].Uptime != "-" {
		t.Fatalf("expected dash uptime for exited container, got %q", rows[2].Uptime)
	}
	if rows[0].Name != "enclave-claude-def456def456" {
		t.Fatalf("expected sorted claude row first, got %q", rows[0].Name)
	}
	if rows[0].Tool != "claude" {
		t.Fatalf("expected tool claude, got %q", rows[0].Tool)
	}
	if rows[0].Directory != "def456def456" {
		t.Fatalf("expected hash fallback, got %q", rows[0].Directory)
	}
	if rows[0].Uptime != "<1m" {
		t.Fatalf("expected short uptime <1m, got %q", rows[0].Uptime)
	}
	if rows[0].Ports != "-" {
		t.Fatalf("expected empty ports marker for session without ports, got %q", rows[0].Ports)
	}
	if rows[1].Name != "enclave-codex-abc123abc123-main" {
		t.Fatalf("expected codex row second, got %q", rows[1].Name)
	}
	if rows[1].Directory != "/tmp/project-alpha" {
		t.Fatalf("expected full worktree path /tmp/project-alpha, got %q", rows[1].Directory)
	}
	if rows[1].Uptime != "1h30m" {
		t.Fatalf("expected 1h30m uptime, got %q", rows[1].Uptime)
	}
	if rows[1].Ports != "http://localhost:3000" {
		t.Fatalf("expected rendered open_url, got %q", rows[1].Ports)
	}
}

func TestFormatPSPorts(t *testing.T) {
	declared := []model.PortConfig{{Container: 3000, Publish: true, OpenURL: "http://localhost:{host_port}"}}
	tests := []struct {
		name     string
		bindings []backend.PortMapping
		declared []model.PortConfig
		want     string
	}{
		{name: "no bindings", want: "-"},
		{
			name:     "declared port renders open_url with host port",
			bindings: []backend.PortMapping{{HostIP: "127.0.0.1", HostPort: "39123", ContainerPort: "3000", Protocol: "tcp"}},
			declared: declared,
			want:     "http://localhost:39123",
		},
		{
			name:     "undeclared binding shows host and port",
			bindings: []backend.PortMapping{{HostIP: "127.0.0.1", HostPort: "8080", ContainerPort: "8080", Protocol: "tcp"}},
			declared: declared,
			want:     "127.0.0.1:8080",
		},
		{
			name:     "no profile falls back to raw binding",
			bindings: []backend.PortMapping{{HostIP: "127.0.0.1", HostPort: "3000", ContainerPort: "3000", Protocol: "tcp"}},
			want:     "127.0.0.1:3000",
		},
		{
			name: "ipv4 and ipv6 duplicates collapse",
			bindings: []backend.PortMapping{
				{HostIP: "0.0.0.0", HostPort: "8080", ContainerPort: "8080", Protocol: "tcp"},
				{HostIP: "::", HostPort: "8080", ContainerPort: "8080", Protocol: "tcp"},
			},
			want: "0.0.0.0:8080",
		},
		{
			name: "multiple ports join",
			bindings: []backend.PortMapping{
				{HostIP: "127.0.0.1", HostPort: "3000", ContainerPort: "3000", Protocol: "tcp"},
				{HostIP: "127.0.0.1", HostPort: "8080", ContainerPort: "8080", Protocol: "tcp"},
			},
			declared: declared,
			want:     "http://localhost:3000, 127.0.0.1:8080",
		},
		{
			name:     "udp binding never renders a url",
			bindings: []backend.PortMapping{{HostIP: "127.0.0.1", HostPort: "3000", ContainerPort: "3000", Protocol: "udp"}},
			declared: declared,
			want:     "127.0.0.1:3000",
		},
		{
			name:     "missing host ip shows all-interfaces bind",
			bindings: []backend.PortMapping{{HostPort: "8080", ContainerPort: "8080", Protocol: "tcp"}},
			want:     "0.0.0.0:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatPSPorts(tt.bindings, tt.declared); got != tt.want {
				t.Fatalf("formatPSPorts() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPSToolAndSessionFiltersOnlyUseExplicitCLIState(t *testing.T) {
	defaults := model.Options{
		RunOptions: model.RunOptions{Tool: "codex", SessionName: "main"},
		Sources:    model.DefaultOptionSources(),
	}
	if got := psToolFilter(defaults); got != "" {
		t.Fatalf("expected default tool not to filter, got %q", got)
	}
	if got := psSessionFilter(defaults); got != "" {
		t.Fatalf("expected default session not to filter, got %q", got)
	}

	explicit := model.Options{
		RunOptions: model.RunOptions{Tool: "codex", SessionName: "main"},
		Sources: model.OptionSources{RunOptionSources: model.RunOptionSources{
			Tool:        model.SourceCLI,
			SessionName: model.SourceCLI,
		}},
	}
	if got := psToolFilter(explicit); got != "codex" {
		t.Fatalf("expected explicit tool filter codex, got %q", got)
	}
	if got := psSessionFilter(explicit); got != "main" {
		t.Fatalf("expected explicit session filter main, got %q", got)
	}
}

func TestDirectoryDisplayName(t *testing.T) {
	tests := []struct {
		name        string
		worktree    string
		projectHash string
		want        string
	}{
		{name: "full path from worktree", worktree: "/tmp/project-alpha", projectHash: "abc123abc123", want: "/tmp/project-alpha"},
		{name: "root path preserved", worktree: "/", projectHash: "abc123abc123", want: "/"},
		{name: "path cleaned", worktree: "/tmp/project-alpha/..", projectHash: "abc123abc123", want: "/tmp"},
		{name: "empty falls back to hash", worktree: "", projectHash: "abc123abc123", want: "abc123abc123"},
		{name: "empty falls back to dash", worktree: "", projectHash: "", want: "-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := directoryDisplayName(tt.worktree, tt.projectHash); got != tt.want {
				t.Fatalf("directoryDisplayName(%q, %q) = %q, want %q", tt.worktree, tt.projectHash, got, tt.want)
			}
		})
	}
}

func TestFormatPSUptime(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		createdAt time.Time
		want      string
	}{
		{name: "zero time", createdAt: time.Time{}, want: "<1m"},
		{name: "under one minute", createdAt: now.Add(-30 * time.Second), want: "<1m"},
		{name: "minutes", createdAt: now.Add(-5 * time.Minute), want: "5m"},
		{name: "whole hours", createdAt: now.Add(-2 * time.Hour), want: "2h"},
		{name: "hours and minutes", createdAt: now.Add(-(2*time.Hour + 5*time.Minute)), want: "2h5m"},
		{name: "whole days", createdAt: now.Add(-48 * time.Hour), want: "2d"},
		{name: "days and hours", createdAt: now.Add(-(48*time.Hour + 3*time.Hour)), want: "2d3h"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatPSUptime(tt.createdAt, now); got != tt.want {
				t.Fatalf("formatPSUptime(%v, %v) = %q, want %q", tt.createdAt, now, got, tt.want)
			}
		})
	}
}

func TestRunPSPrintsTable(t *testing.T) {
	psCheckDocker = func() error { return nil }
	psNow = func() time.Time {
		return time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	}
	psSessionList = func(context.Context, model.Options) ([]backend.Session, error) {
		return []backend.Session{{
			Ref:         backend.SessionRef{Name: "enclave-codex-abc123abc123-main"},
			Tool:        "codex",
			ProjectHash: "abc123abc123",
			Worktree:    "/tmp/project-alpha",
			Status:      "running",
			Name:        "main",
			Ports:       []backend.PortMapping{{HostIP: "127.0.0.1", HostPort: "3000", ContainerPort: "3000", Protocol: "tcp"}},
		}}, nil
	}
	psDeclaredPorts = func(string) []model.PortConfig {
		return []model.PortConfig{{Container: 3000, Publish: true, OpenURL: "http://localhost:{host_port}"}}
	}
	t.Cleanup(func() {
		psCheckDocker = checkDocker
		psSessionList = listPSSessions
		psNow = time.Now
		psDeclaredPorts = declaredPublishedPorts
	})

	out := captureStdout(t, func() {
		if code := runPS(model.Options{}); code != 0 {
			t.Fatalf("runPS() returned %d", code)
		}
	})

	if !strings.Contains(out, "NAME") {
		t.Fatalf("expected table header in output, got:\n%s", out)
	}
	if !strings.Contains(out, "DIR") {
		t.Fatalf("expected dir header in output, got:\n%s", out)
	}
	if !strings.Contains(out, "PORTS") {
		t.Fatalf("expected ports header in output, got:\n%s", out)
	}
	if !strings.Contains(out, "enclave-codex-abc123abc123-main") {
		t.Fatalf("expected container row in output, got:\n%s", out)
	}
	if !strings.Contains(out, "/tmp/project-alpha") {
		t.Fatalf("expected full dir path in output, got:\n%s", out)
	}
	if !strings.Contains(out, "http://localhost:3000") {
		t.Fatalf("expected rendered open_url in output, got:\n%s", out)
	}
	if strings.Contains(out, "SESSION") {
		t.Fatalf("did not expect session header in output, got:\n%s", out)
	}
}

func TestBuildPSJSONEntries(t *testing.T) {
	created := time.Date(2026, 3, 19, 10, 30, 0, 0, time.UTC)
	entries := buildPSJSONEntries([]backend.Session{
		{
			Ref:         backend.SessionRef{Name: "enclave-codex-abc123abc123-main"},
			Tool:        "codex",
			ProjectHash: "abc123abc123",
			Worktree:    "/tmp/project-alpha",
			ProjectDir:  "/tmp/project-alpha",
			Status:      "exited",
			CreatedAt:   created,
			Name:        "main",
			Background:  true,
			Ports:       []backend.PortMapping{{HostIP: "127.0.0.1", HostPort: "3000", ContainerPort: "3000", Protocol: "tcp"}},
		},
		{
			Ref:         backend.SessionRef{Name: "enclave-claude-def456def456"},
			Tool:        "claude",
			ProjectHash: "def456def456",
			ProjectDir:  "/tmp/project-beta",
		},
	})

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Name != "enclave-claude-def456def456" {
		t.Fatalf("expected sorted claude entry first, got %q", entries[0].Name)
	}
	// Empty status normalizes to running; empty createdAt stays empty.
	if entries[0].Status != "running" {
		t.Fatalf("expected running status, got %q", entries[0].Status)
	}
	if entries[0].CreatedAt != "" {
		t.Fatalf("expected empty createdAt for zero time, got %q", entries[0].CreatedAt)
	}
	if entries[0].ProjectDir != "/tmp/project-beta" {
		t.Fatalf("expected projectDir /tmp/project-beta, got %q", entries[0].ProjectDir)
	}

	codex := entries[1]
	if codex.Status != "exited" {
		t.Fatalf("expected exited status preserved, got %q", codex.Status)
	}
	if codex.CreatedAt != "2026-03-19T10:30:00Z" {
		t.Fatalf("expected RFC3339 createdAt, got %q", codex.CreatedAt)
	}
	if !codex.Background {
		t.Fatalf("expected background true")
	}
	if codex.SessionName != "main" {
		t.Fatalf("expected sessionName main, got %q", codex.SessionName)
	}
	if len(codex.Ports) != 1 {
		t.Fatalf("expected 1 port binding, got %d", len(codex.Ports))
	}
	if got := codex.Ports[0]; got.ContainerPort != "3000" || got.HostPort != "3000" || got.HostIP != "127.0.0.1" || got.Protocol != "tcp" {
		t.Fatalf("unexpected port binding: %+v", got)
	}
	// Sessions without ports emit an empty array, not null.
	if entries[0].Ports == nil {
		t.Fatalf("expected non-nil empty ports slice for session without ports")
	}
}

func TestRunPSEmitsJSON(t *testing.T) {
	psCheckDocker = func() error { return nil }
	psNow = func() time.Time { return time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC) }
	psSessionList = func(context.Context, model.Options) ([]backend.Session, error) {
		return []backend.Session{{
			Ref:         backend.SessionRef{Name: "enclave-codex-abc123abc123-main"},
			Tool:        "codex",
			ProjectHash: "abc123abc123",
			Worktree:    "/tmp/project-alpha",
			ProjectDir:  "/tmp/project-alpha",
			Status:      "running",
			Name:        "main",
		}}, nil
	}
	t.Cleanup(func() {
		psCheckDocker = checkDocker
		psSessionList = listPSSessions
		psNow = time.Now
	})

	out := captureStdout(t, func() {
		if code := runPS(model.Options{PSOptions: model.PSOptions{PSJSON: true}}); code != 0 {
			t.Fatalf("runPS() returned %d", code)
		}
	})

	var entries []psJSONEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("expected valid JSON output, got error %v for:\n%s", err, out)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ProjectDir != "/tmp/project-alpha" {
		t.Fatalf("expected projectDir in JSON, got %q", entries[0].ProjectDir)
	}
	if strings.Contains(out, "NAME") {
		t.Fatalf("did not expect table header in JSON output, got:\n%s", out)
	}
}

func TestRunPSEmitsEmptyJSONArray(t *testing.T) {
	psCheckDocker = func() error { return nil }
	psNow = func() time.Time { return time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC) }
	psSessionList = func(context.Context, model.Options) ([]backend.Session, error) { return nil, nil }
	t.Cleanup(func() {
		psCheckDocker = checkDocker
		psSessionList = listPSSessions
		psNow = time.Now
	})

	out := captureStdout(t, func() {
		if code := runPS(model.Options{PSOptions: model.PSOptions{PSJSON: true}}); code != 0 {
			t.Fatalf("runPS() returned %d", code)
		}
	})

	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("expected empty JSON array, got:\n%s", out)
	}
}

func TestPSSessionFilterForHonorsAll(t *testing.T) {
	running := psSessionFilterFor(model.Options{})
	if !running.RunningOnly || running.All {
		t.Fatalf("expected running-only filter, got %+v", running)
	}

	all := psSessionFilterFor(model.Options{PSOptions: model.PSOptions{PSAll: true}})
	if all.RunningOnly || !all.All {
		t.Fatalf("expected all filter, got %+v", all)
	}
}

func TestRunPSAllEmptyMessage(t *testing.T) {
	psCheckDocker = func() error { return nil }
	psNow = func() time.Time { return time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC) }
	psSessionList = func(context.Context, model.Options) ([]backend.Session, error) { return nil, nil }
	t.Cleanup(func() {
		psCheckDocker = checkDocker
		psSessionList = listPSSessions
		psNow = time.Now
	})

	out := captureStdout(t, func() {
		if code := runPS(model.Options{PSOptions: model.PSOptions{PSAll: true}}); code != 0 {
			t.Fatalf("runPS() returned %d", code)
		}
	})

	if !strings.Contains(out, "No enclave containers found") || strings.Contains(out, "No running") {
		t.Fatalf("expected all-mode empty message, got:\n%s", out)
	}
}

func TestRunPSPrintsEmptyMessage(t *testing.T) {
	psCheckDocker = func() error { return nil }
	psNow = func() time.Time {
		return time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	}
	psSessionList = func(context.Context, model.Options) ([]backend.Session, error) { return nil, nil }
	t.Cleanup(func() {
		psCheckDocker = checkDocker
		psSessionList = listPSSessions
		psNow = time.Now
	})

	out := captureStdout(t, func() {
		if code := runPS(model.Options{}); code != 0 {
			t.Fatalf("runPS() returned %d", code)
		}
	})

	if !strings.Contains(out, "No running enclave containers found") {
		t.Fatalf("expected empty-state output, got:\n%s", out)
	}
}
