// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"enclave/internal/backend"
	backendvm "enclave/internal/backend/vm"
	"enclave/internal/config"
	"enclave/internal/model"
)

// A backend that runs one session per project and tool refuses the second
// before the runtime resets auth or overlays the config store the first one
// is using.
func TestPrepareExecutionAsksTheBackendBeforeAnySetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	refused := errors.New("disk in use by another session")
	var asked []backend.SessionMeta
	fake := &fakeBackend{admit: func(meta backend.SessionMeta) error {
		asked = append(asked, meta)
		return refused
	}}
	r := &Runtime{
		backend: fake,
		host:    model.Host{Home: home},
		project: model.Project{Hash: "abc123def456", Dir: t.TempDir()},
		profile: model.Profile{Name: "codex"},
		auth:    model.AuthOptions{ResetAuth: true},
	}
	if _, err := r.prepareExecution(); !errors.Is(err, refused) {
		t.Fatalf("prepareExecution = %v, want the backend's refusal", err)
	}
	if len(asked) != 1 || asked[0].Tool != "codex" || asked[0].ProjectHash != "abc123def456" {
		t.Fatalf("backend was asked %+v", asked)
	}
	if len(fake.prepared) != 0 {
		t.Fatalf("stores were prepared for a refused session: %+v", fake.prepared)
	}
	if _, err := os.Stat(config.HostProjectsDir(home)); !os.IsNotExist(err) {
		t.Fatalf("project state was written for a refused session: %v", err)
	}
}

// The same through the vm backend itself: while one enclave process holds a
// project's disk, a second run with --reset-auth is refused and the auth file
// the running session uses is still there.
func TestRefusedVMSessionLeavesStoresAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	host := model.Host{Home: home}
	project := model.Project{Hash: "abc123def456", Dir: t.TempDir()}
	profile := model.Profile{Name: "codex"}
	running := backendvm.New(backendvm.Options{Host: host})
	meta := backend.SessionMeta{Tool: profile.Name, ProjectHash: project.Hash}
	if err := running.AdmitSession(context.Background(), meta); err != nil {
		t.Fatal(err)
	}
	key := backend.StoreKey{Owner: profile.Name, ProjectHash: project.Hash}
	if err := running.Storage().WriteFile(context.Background(), key, backend.StoreKindConfig, "auth.json", []byte("live credential"), 0o600); err != nil {
		t.Fatal(err)
	}

	second := &Runtime{
		backend: backendvm.New(backendvm.Options{Host: host}),
		host:    host,
		project: project,
		profile: profile,
		auth:    model.AuthOptions{ResetAuth: true},
	}
	if _, err := second.prepareExecution(); err == nil || !strings.Contains(err.Error(), "in use by another session") {
		t.Fatalf("prepareExecution = %v, want the disk-in-use refusal", err)
	}
	got, err := running.Storage().ReadFile(context.Background(), key, backend.StoreKindConfig, "auth.json")
	if err != nil || string(got) != "live credential" {
		t.Fatalf("the running session's auth file after a refused start: %q, %v", got, err)
	}
}
