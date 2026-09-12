// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"strings"
	"testing"

	"enclave/internal/model"
)

func TestPrepareImageUserNamespaceWarmsKeepIDLayerUnderPodman(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	logPath := stubCLI(t, "podman", "case \"$1\" in create) echo cid ;; esac\nexit 0\n")
	b := New(Options{Host: model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}})

	if err := b.prepareImageUserNamespace(context.Background(), "enclave-test:latest"); err != nil {
		t.Fatalf("prepareImageUserNamespace: %v", err)
	}
	calls := stubCalls(t, logPath)
	if len(calls) != 2 {
		t.Fatalf("expected a create followed by a remove, got %v", calls)
	}
	for _, want := range []string{"create ", "--userns keep-id", "--entrypoint true", " enclave-test:latest"} {
		if !strings.Contains(calls[0], want) {
			t.Fatalf("create call %q lacks %q", calls[0], want)
		}
	}
	if !strings.HasPrefix(calls[1], "rm --force --volumes "+model.AppName+"-userns-warmup-") {
		t.Fatalf("expected the warm-up container to be removed, got %q", calls[1])
	}
}

func TestPrepareImageUserNamespaceIsNoopUnderDocker(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	logPath := stubCLI(t, "docker", "exit 0\n")
	b := New(Options{Host: model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}})

	if err := b.prepareImageUserNamespace(context.Background(), "enclave-test:latest"); err != nil {
		t.Fatalf("prepareImageUserNamespace: %v", err)
	}
	if calls := stubCalls(t, logPath); len(calls) != 0 {
		t.Fatalf("docker needs no user namespace preparation, got %v", calls)
	}
}

func TestPrepareImageUserNamespaceReportsCreateFailureAfterCleanup(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	logPath := stubCLI(t, "podman", "case \"$1\" in create) echo 'Error: boom' >&2; exit 125 ;; esac\nexit 0\n")
	b := New(Options{Host: model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}})

	err := b.prepareImageUserNamespace(context.Background(), "enclave-test:latest")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected the create failure to surface, got %v", err)
	}
	if calls := stubCalls(t, logPath); !hasCallWithPrefix(calls, "rm --force --volumes ") {
		t.Fatalf("expected a removal attempt even after a failed create, got %v", calls)
	}
}
