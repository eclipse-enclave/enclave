// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/docker"
	"enclave/internal/model"
)

// withContainerCLI points the CLI wrapper at the named engine for one test.
func withContainerCLI(t *testing.T, name string) {
	t.Helper()
	previous := docker.Binary()
	docker.SetBinary(name)
	t.Cleanup(func() { docker.SetBinary(previous) })
}

func TestResolveBuildxCacheDropsSpecsUnderPodman(t *testing.T) {
	opts := model.BuildOptions{
		BuildxCacheDir:  filepath.Join(t.TempDir(), "cache"),
		BuildxCacheFrom: []string{"type=local,src=/x"},
		BuildxCacheTo:   []string{"type=local,dest=/y"},
	}

	withContainerCLI(t, backend.NameDocker)
	from, to, err := resolveBuildxCache(opts)
	if err != nil {
		t.Fatalf("resolveBuildxCache under docker: %v", err)
	}
	if len(from) == 0 || len(to) == 0 {
		t.Fatalf("docker must keep the buildx cache specs, got from=%v to=%v", from, to)
	}

	opts.BuildxCacheDir = filepath.Join(t.TempDir(), "cache")
	withContainerCLI(t, backend.NamePodman)
	from, to, err = resolveBuildxCache(opts)
	if err != nil {
		t.Fatalf("resolveBuildxCache under podman: %v", err)
	}
	if from != nil || to != nil {
		t.Fatalf("podman must drop the buildx cache specs, got from=%v to=%v", from, to)
	}
	if _, err := os.Stat(opts.BuildxCacheDir); !os.IsNotExist(err) {
		t.Fatalf("podman must not create the buildx cache directory, stat err=%v", err)
	}
}

func TestCheckRuntimeImageBuildPreflightSkipsBuildxCheckUnderPodman(t *testing.T) {
	stubBuildxAvailable(t, false)
	stubDockerRootFreeSpace(t, func(context.Context) (string, uint64, error) {
		return "/home/user/.local/share/containers/storage", 20 * 1024 * 1024 * 1024, nil
	})
	withContainerCLI(t, backend.NamePodman)

	if err := checkRuntimeImageBuildPreflight(context.Background()); err != nil {
		t.Fatalf("podman builds with buildah and must not require buildx, got %v", err)
	}
}
