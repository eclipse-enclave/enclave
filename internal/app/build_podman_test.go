// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/docker"
)

func TestStripHomeCacheMountsKeepsRunStepsIntact(t *testing.T) {
	repoDockerfile := filepath.Join("..", "..", "Dockerfile")
	if _, err := os.Stat(repoDockerfile); err != nil {
		t.Skipf("repo Dockerfile not available: %v", err)
	}
	rendered, err := renderDockerfile(repoDockerfile, []string{"claude"}, []featureInstall{
		{Name: "devtools", Priority: 40, HasApt: true, HasScript: true},
	}, nil, nil)
	if err != nil {
		t.Fatalf("renderDockerfile: %v", err)
	}
	if !strings.Contains(rendered, "target=/home/${USERNAME}/.npm") {
		t.Fatal("test premise: rendered Dockerfile should mount the npm cache under the agent home")
	}

	got := stripHomeCacheMounts(rendered)
	if strings.Contains(got, "target=/home/") {
		t.Fatalf("home cache mounts survived:\n%s", got)
	}
	if !strings.Contains(got, "--mount=type=cache,id=enclave-apt-cache,target=/var/cache/apt") {
		t.Fatal("apt cache mounts outside the home must be preserved")
	}
	for _, want := range []string{
		"ENCLAVE_FEATURE_PHASE=user",
		"enclave-install-tool",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stripped Dockerfile lost %q", want)
		}
	}
	for i, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == "\\" {
			t.Fatalf("line %d holds only a continuation backslash", i+1)
		}
	}
	if stripHomeCacheMounts("FROM x\nRUN true\n") != "FROM x\nRUN true\n" {
		t.Fatal("Dockerfiles without home cache mounts must pass through unchanged")
	}
}

func TestRenderEngineDockerfileStripsHomeCacheMountsOnlyForPodman(t *testing.T) {
	repoDockerfile := filepath.Join("..", "..", "Dockerfile")
	if _, err := os.Stat(repoDockerfile); err != nil {
		t.Skipf("repo Dockerfile not available: %v", err)
	}
	previous := docker.Binary()
	t.Cleanup(func() { docker.SetBinary(previous) })

	forDocker, err := renderEngineDockerfile(repoDockerfile, []string{"claude"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("renderEngineDockerfile (docker): %v", err)
	}
	if !strings.Contains(forDocker, "target=/home/${USERNAME}/.npm") {
		t.Fatal("docker builds must keep the home cache mounts")
	}

	docker.SetBinary(backend.NamePodman)
	forPodman, err := renderEngineDockerfile(repoDockerfile, []string{"claude"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("renderEngineDockerfile (podman): %v", err)
	}
	if strings.Contains(forPodman, "target=/home/") {
		t.Fatal("podman builds must not mount caches under the agent home")
	}
}
