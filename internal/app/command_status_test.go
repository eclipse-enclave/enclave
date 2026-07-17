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
	"fmt"
	"strings"
	"testing"
	"time"

	"enclave/internal/backend"
)

type fakeOutputExecer struct {
	screens  map[string]string
	titles   map[string]string
	errs     map[string]error
	calls    int
	lastUser string
}

func (f *fakeOutputExecer) ExecOutput(_ context.Context, ref backend.SessionRef, argv []string, user string) (string, error) {
	f.calls++
	f.lastUser = user
	if err := f.errs[ref.Name]; err != nil {
		return "", err
	}
	joined := strings.Join(argv, " ")
	if !strings.HasPrefix(joined, "tmux -L enclave ") {
		return "", fmt.Errorf("unexpected tmux invocation %v", argv)
	}
	switch {
	case strings.Contains(joined, "capture-pane"):
		return f.screens[ref.Name], nil
	case strings.Contains(joined, "display-message"):
		return f.titles[ref.Name], nil
	}
	return "", fmt.Errorf("unexpected argv %v", argv)
}

func statusTestNow() time.Time {
	return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
}

func TestCaptureSnapshotTmux(t *testing.T) {
	screen := "● Done.\n\n❯\n"
	execer := &fakeOutputExecer{
		screens: map[string]string{"enclave-claude-abc123abc123-main": screen},
		titles:  map[string]string{"enclave-claude-abc123abc123-main": "✳ Gate latency collector\n"},
	}
	session := backend.Session{
		Ref:                backend.SessionRef{Name: "enclave-claude-abc123abc123-main"},
		Tool:               "claude",
		Status:             "running",
		Name:               "main",
		SessionMonitor:     true,
		SessionMonitorUser: "agent",
	}

	snap := captureSnapshot(context.Background(), execer, session, 0, statusTestNow)

	if snap.Capture != captureTmux {
		t.Fatalf("expected capture %q, got %q", captureTmux, snap.Capture)
	}
	if execer.lastUser != "agent" {
		t.Fatalf("expected capture to run as the recorded tmux owner, got %q", execer.lastUser)
	}
	if snap.Screen != screen {
		t.Fatalf("expected screen kept verbatim, got %q", snap.Screen)
	}
	if snap.OSCTitle != "✳ Gate latency collector" {
		t.Fatalf("expected trailing newline trimmed from title, got %q", snap.OSCTitle)
	}
	if snap.Agent != "claude" || snap.SessionID != "enclave-claude-abc123abc123-main" || snap.SessionName != "main" {
		t.Fatalf("unexpected identity fields: %+v", snap)
	}
	if snap.Timestamp != statusTestNow().UnixMilli() {
		t.Fatalf("expected timestamp %d, got %d", statusTestNow().UnixMilli(), snap.Timestamp)
	}
	if snap.Error != "" {
		t.Fatalf("expected no error, got %q", snap.Error)
	}
}

func TestCaptureSnapshotWithoutSessionMonitorIsUnavailable(t *testing.T) {
	execer := &fakeOutputExecer{}
	session := backend.Session{
		Ref:    backend.SessionRef{Name: "enclave-codex-def456def456"},
		Tool:   "codex",
		Status: "running",
	}

	snap := captureSnapshot(context.Background(), execer, session, 0, statusTestNow)

	if snap.Capture != captureUnavailable {
		t.Fatalf("expected capture %q, got %q", captureUnavailable, snap.Capture)
	}
	if execer.calls != 0 {
		t.Fatalf("expected no exec calls for unmonitored session, got %d", execer.calls)
	}
	if snap.Screen != "" || snap.OSCTitle != "" || snap.Error != "" {
		t.Fatalf("expected empty snapshot content, got %+v", snap)
	}
}

func TestCaptureSnapshotExecErrorDegrades(t *testing.T) {
	execer := &fakeOutputExecer{
		errs: map[string]error{"enclave-claude-abc123abc123": fmt.Errorf("tmux not running")},
	}
	session := backend.Session{
		Ref:            backend.SessionRef{Name: "enclave-claude-abc123abc123"},
		Tool:           "claude",
		SessionMonitor: true,
	}

	snap := captureSnapshot(context.Background(), execer, session, 0, statusTestNow)

	if snap.Capture != captureUnavailable {
		t.Fatalf("expected capture %q, got %q", captureUnavailable, snap.Capture)
	}
	if snap.Error != "tmux not running" {
		t.Fatalf("expected error surfaced, got %q", snap.Error)
	}
}

func TestCollectSnapshotsSortsBySessionID(t *testing.T) {
	sessions := []backend.Session{
		{Ref: backend.SessionRef{Name: "enclave-codex-b"}, Tool: "codex"},
		{Ref: backend.SessionRef{Name: "enclave-claude-a"}, Tool: "claude"},
	}

	snapshots := collectSnapshots(context.Background(), &fakeOutputExecer{}, sessions, 0, statusTestNow)

	if len(snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snapshots))
	}
	if snapshots[0].SessionID != "enclave-claude-a" || snapshots[1].SessionID != "enclave-codex-b" {
		t.Fatalf("expected snapshots sorted by session id, got %q, %q", snapshots[0].SessionID, snapshots[1].SessionID)
	}
}

func TestSnapshotJSONShape(t *testing.T) {
	execer := &fakeOutputExecer{
		screens: map[string]string{"enclave-claude-a": "❯\n"},
		titles:  map[string]string{"enclave-claude-a": "✳ idle"},
	}
	session := backend.Session{
		Ref:            backend.SessionRef{Name: "enclave-claude-a"},
		Tool:           "claude",
		Status:         "running",
		SessionMonitor: true,
	}

	data, err := json.Marshal(captureSnapshot(context.Background(), execer, session, 0, statusTestNow))
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	for _, key := range []string{"agent", "session_id", "timestamp", "screen", "osc_title", "osc_progress", "capture"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("expected key %q in snapshot JSON, got %s", key, data)
		}
	}
	if decoded["osc_progress"] != nil {
		t.Fatalf("expected osc_progress to be null, got %v", decoded["osc_progress"])
	}
	if _, ok := decoded["error"]; ok {
		t.Fatalf("expected error omitted when empty, got %s", data)
	}
}

func TestCaptureSnapshotCapsScreenLines(t *testing.T) {
	execer := &fakeOutputExecer{
		screens: map[string]string{"enclave-claude-a": "row1\nrow2\nrow3\nrow4\n"},
		titles:  map[string]string{"enclave-claude-a": "t"},
	}
	session := backend.Session{
		Ref:            backend.SessionRef{Name: "enclave-claude-a"},
		Tool:           "claude",
		SessionMonitor: true,
	}

	snap := captureSnapshot(context.Background(), execer, session, 2, statusTestNow)

	if snap.Screen != "row3\nrow4\n" {
		t.Fatalf("expected screen capped to trailing 2 lines, got %q", snap.Screen)
	}
}

func TestTailLines(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "cap", in: "a\nb\nc\nd\n", n: 2, want: "c\nd\n"},
		{name: "fewer lines than cap", in: "a\nb\n", n: 24, want: "a\nb\n"},
		{name: "zero keeps all", in: "a\nb\n", n: 0, want: "a\nb\n"},
		{name: "no trailing newline", in: "a\nb\nc", n: 2, want: "b\nc"},
		{name: "blank rows preserved", in: "a\n\n\n❯\n", n: 3, want: "\n\n❯\n"},
		{name: "empty", in: "", n: 24, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tailLines(tc.in, tc.n); got != tc.want {
				t.Fatalf("tailLines(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestLastScreenLine(t *testing.T) {
	if got := lastScreenLine("● Done.\n\n❯ run tests\n   \n"); got != "❯ run tests" {
		t.Fatalf("expected bottommost non-blank line, got %q", got)
	}
	if got := lastScreenLine("\n \n"); got != "" {
		t.Fatalf("expected empty result for blank screen, got %q", got)
	}
}

func TestTableCellSanitizesWhitespace(t *testing.T) {
	if got := tableCell(""); got != "-" {
		t.Fatalf("expected placeholder for empty cell, got %q", got)
	}
	if got := tableCell("a\tb\nc\rd"); got != "a b c d" {
		t.Fatalf("expected tabs/newlines collapsed to spaces, got %q", got)
	}
}

func TestRenderStatusTableTitleWithTabStaysAligned(t *testing.T) {
	var out strings.Builder
	err := renderStatusTable(&out, []Snapshot{
		{SessionID: "enclave-claude-a", Agent: "claude", Capture: captureTmux, OSCTitle: "col1\tcol2", Screen: "❯\n"},
	})
	if err != nil {
		t.Fatalf("render table: %v", err)
	}
	// With the tab collapsed to a single space the title stays in one cell;
	// an unsanitized tab would open an extra column and pad it to "col1  col2".
	if !strings.Contains(out.String(), "col1 col2") {
		t.Fatalf("expected title kept in a single cell, got %q", out.String())
	}
}

func TestRenderStatusTable(t *testing.T) {
	var out strings.Builder
	err := renderStatusTable(&out, []Snapshot{
		{SessionID: "enclave-claude-a", Agent: "claude", Capture: captureTmux, OSCTitle: "✳ idle", Screen: "❯\n"},
		{SessionID: "enclave-codex-b", Agent: "codex", Capture: captureUnavailable},
	})
	if err != nil {
		t.Fatalf("render table: %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "NAME") || !strings.Contains(rendered, "LAST LINE") {
		t.Fatalf("expected header row, got %q", rendered)
	}
	if !strings.Contains(rendered, "✳ idle") {
		t.Fatalf("expected title cell, got %q", rendered)
	}
	if !strings.Contains(rendered, "unavailable") {
		t.Fatalf("expected unavailable capture cell, got %q", rendered)
	}
}
