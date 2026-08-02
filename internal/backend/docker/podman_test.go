// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"testing"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
)

func podmanTestBackend(t *testing.T) *Backend {
	t.Helper()
	return New(Options{
		Host:   model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"},
		Engine: dockercmd.EnginePodman,
	})
}

func TestPodmanBackendName(t *testing.T) {
	if got := podmanTestBackend(t).Name(); got != backend.NamePodman {
		t.Fatalf("backend name = %q, want %q", got, backend.NamePodman)
	}
	if got := sandboxTestBackend(t).Name(); got != backend.NameDocker {
		t.Fatalf("backend name = %q, want %q", got, backend.NameDocker)
	}
}

// The docker rootless sandbox must never engage under podman: rootless podman
// sessions run with --userns=keep-id instead.
func TestApplyRootlessSandboxSkipsPodman(t *testing.T) {
	stubRootless(t, true)
	b := podmanTestBackend(t)
	spec := sandboxTestSpec()

	if err := b.applyRootlessSandbox(context.Background(), backend.Request{}, spec); err != nil {
		t.Fatalf("apply rootless sandbox: %v", err)
	}
	if spec.config.User != "" {
		t.Fatalf("expected user untouched under podman, got %q", spec.config.User)
	}
	if len(spec.config.Env) != 0 || len(spec.hostConfig.SecurityOpt) != 0 {
		t.Fatalf("expected no sandbox env or security opts under podman, got %v / %v", spec.config.Env, spec.hostConfig.SecurityOpt)
	}
}

func TestWrapSandboxExecPassthroughPodman(t *testing.T) {
	stubRootless(t, true)
	b := podmanTestBackend(t)
	argv, user := b.wrapSandboxExec(context.Background(), []string{"bash"}, "")
	if len(argv) != 1 || argv[0] != "bash" || user != "" {
		t.Fatalf("expected passthrough exec under podman, got argv=%v user=%q", argv, user)
	}
}

func TestPodmanRootlessDetection(t *testing.T) {
	stubRootless(t, true)
	rootless, err := podmanTestBackend(t).podmanRootless(context.Background())
	if err != nil {
		t.Fatalf("podman rootless: %v", err)
	}
	if !rootless {
		t.Fatalf("expected rootless podman detection")
	}
	rootless, err = sandboxTestBackend(t).podmanRootless(context.Background())
	if err != nil {
		t.Fatalf("docker backend podman rootless: %v", err)
	}
	if rootless {
		t.Fatalf("docker engine must never report podman rootless")
	}
}
