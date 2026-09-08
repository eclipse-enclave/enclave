// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"testing"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
)

func usePodmanCLI(t *testing.T) {
	t.Helper()
	previous := dockercmd.Binary()
	dockercmd.SetBinary(backend.NamePodman)
	t.Cleanup(func() { dockercmd.SetBinary(previous) })
}

func TestPodmanSessionsKeepHostUserNamespace(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := New(Options{Host: model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}})
	req := backend.Request{
		Session: backend.SessionMeta{Tool: "codex", ProjectHash: "abc123abc123", Name: "enclave-codex-abc123abc123"},
		Image:   "enclave-test:latest",
	}

	if spec := b.dockerConfig(req); spec.hostConfig.UserNS != "" {
		t.Fatalf("docker sessions must not set --userns, got %q", spec.hostConfig.UserNS)
	}
	if got := b.Name(); got != backend.NameDocker {
		t.Fatalf("Name() = %q, want %q", got, backend.NameDocker)
	}

	usePodmanCLI(t)
	if spec := b.dockerConfig(req); spec.hostConfig.UserNS != "keep-id" {
		t.Fatalf("podman sessions must run with --userns keep-id, got %q", spec.hostConfig.UserNS)
	}
	if got := b.Name(); got != backend.NamePodman {
		t.Fatalf("Name() = %q, want %q", got, backend.NamePodman)
	}

	helper := sharedAuthSyncHostConfig("/config", "/auth", "/script.sh", false)
	applyHostUserNamespace(helper)
	if helper.UserNS != "keep-id" {
		t.Fatalf("auth reconcile helper must run with --userns keep-id under podman, got %q", helper.UserNS)
	}
	if got := b.authSyncHelperUser(); got != "1000:1000" {
		t.Fatalf("auth reconcile helper must run as the host user under podman, got %q", got)
	}
}

func TestAuthSyncHelperRunsAsRootUnderDocker(t *testing.T) {
	b := New(Options{Host: model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}})
	if got := b.authSyncHelperUser(); got != "root" {
		t.Fatalf("auth reconcile helper user = %q, want root", got)
	}
}
