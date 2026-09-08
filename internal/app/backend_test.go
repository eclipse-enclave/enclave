// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"strings"
	"testing"

	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/docker"
	"enclave/internal/model"
)

func TestSelectBackendPodmanUsesDockerBackendOverPodmanCLI(t *testing.T) {
	previous := docker.Binary()
	t.Cleanup(func() { docker.SetBinary(previous) })

	opts := model.Options{RunOptions: model.RunOptions{Backend: backend.NamePodman}}
	selectContainerCLI(opts.Backend)
	if docker.Binary() != backend.NamePodman {
		t.Fatalf("container CLI = %q, want podman", docker.Binary())
	}

	be, err := selectBackend(opts, backenddocker.Options{})
	if err != nil {
		t.Fatalf("selectBackend: %v", err)
	}
	if be.Name() != backend.NamePodman {
		t.Fatalf("backend name = %q, want %q", be.Name(), backend.NamePodman)
	}

	if _, err := selectBackend(model.Options{RunOptions: model.RunOptions{Backend: "lxc"}}, backenddocker.Options{}); err == nil || !strings.Contains(err.Error(), "podman") {
		t.Fatalf("expected unsupported-backend error listing podman, got %v", err)
	}
}

func TestSelectContainerCLIKeepsDockerForOtherBackends(t *testing.T) {
	previous := docker.Binary()
	t.Cleanup(func() { docker.SetBinary(previous) })
	for _, name := range []string{"", backend.NameDocker, backend.NameQEMU} {
		selectContainerCLI(name)
		if docker.Binary() != previous {
			t.Fatalf("backend %q switched the container CLI to %q", name, docker.Binary())
		}
	}
}

func TestValidateOptionsAcceptsPodmanBackend(t *testing.T) {
	opts := model.Options{
		RunOptions:   model.RunOptions{Backend: backend.NamePodman, Tool: "claude"},
		BuildOptions: model.BuildOptions{ImageName: "test"},
	}
	got, _, _, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Action: "run"})
	if err != nil {
		t.Fatalf("podman backend should validate, got %v", err)
	}
	if got.Backend != backend.NamePodman {
		t.Fatalf("Backend = %q, want podman", got.Backend)
	}
	if got.AllowAllNetwork || got.Slim {
		t.Fatal("podman must keep the full docker feature set (no qemu coercion)")
	}

	opts.Devcontainer = true
	if _, _, _, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Action: "run"}); err == nil {
		t.Fatal("devcontainer mode is only verified with docker and must be rejected for podman")
	}
}
