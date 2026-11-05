// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

var updatePlainSessionGolden = flag.Bool("update-plain-session-golden", false, "regenerate testdata/plain_session_request.golden.json")

// recordingBackend keeps what a session start hands to the backend.
type recordingBackend struct {
	fakeBackend
	requests []backend.Request
	attaches []backend.AttachIO
}

func (b *recordingBackend) Run(_ context.Context, req backend.Request, attach backend.AttachIO) (backend.ExitStatus, error) {
	b.requests = append(b.requests, req)
	b.attaches = append(b.attaches, attach)
	return backend.ExitStatus{}, nil
}

// plainSessionRuntime is a Claude session without --peer-group; its A2A
// options carry the defaults every session records.
func plainSessionRuntime(be backend.Backend) *Runtime {
	return &Runtime{
		profile: model.Profile{
			Name:         "claude",
			Command:      "claude",
			YoloFlag:     "--dangerously-skip-permissions",
			ConfigDir:    ".claude",
			NoMemoryArgs: []string{"--settings", `{"autoMemoryEnabled": false}`},
			A2A: &model.A2ASpec{
				Command:     []string{"claude", "-p"},
				ResumeArgs:  []string{"--resume", model.A2ASessionIDPlaceholder},
				EventFormat: model.A2AEventFormatClaudeStreamJSON,
			},
		},
		project:     model.Project{Hash: "abc123abc123", Dir: "/work/project", RealDir: "/work/project", Name: "project"},
		build:       model.BuildOptions{ImageName: "enclave-claude:latest"},
		yoloEnabled: true,
		projectDir:  "/work/project",
		run: model.RunOptions{
			CmdArgs:     []string{"--resume", "fix the bug"},
			NoMemory:    true,
			SessionName: "review",
			A2AOptions: model.A2AOptions{
				A2ARole:       "worker",
				A2ALog:        "coarse",
				A2AContextTTL: "24h",
			},
		},
		backend: be,
	}
}

type plainSessionSnapshot struct {
	Foreground       backend.Request
	ForegroundTTY    bool
	ForegroundOutSet bool
	Background       backend.Request
}

// The golden was recorded on the a2a branch before I1 wired peer groups, so
// a plain session's start must stay byte-identical.
func TestPlainSessionBackendRequestUnchanged(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "")
	be := &recordingBackend{}
	r := plainSessionRuntime(be)
	ec := &ExecutionContext{
		ContainerName: "enclave-claude-abc123abc123-review",
		Env:           []string{"TZ=UTC", "FOO=bar"},
		Mounts:        []backend.Mount{{Source: "/host/src", ContainerPath: "/work/project"}},
		Network:       backend.NetworkPolicy{Mode: backend.NetworkModeRestricted},
	}
	if err := r.runContainer(context.Background(), ec, func() {}); err != nil {
		t.Fatalf("runContainer: %v", err)
	}
	if len(be.requests) != 1 {
		t.Fatalf("backend saw %d runs, want 1", len(be.requests))
	}
	snapshot := plainSessionSnapshot{
		Foreground:       be.requests[0],
		ForegroundTTY:    be.attaches[0].TTY,
		ForegroundOutSet: be.attaches[0].Out != nil,
		Background:       r.backendRequest(ec, true, true),
	}
	got, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "plain_session_request.golden.json")
	if *updatePlainSessionGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("plain session request changed:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
