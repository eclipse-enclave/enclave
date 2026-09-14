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

// The build-time package caches must never mount under the agent home: buildah
// commits the ancestors of a cache mount target as root-owned, which left the
// agent unable to write its home on podman. Keeping them under
// /var/cache/enclave lets one Dockerfile serve both engines.
func TestRenderedDockerfileMountsPackageCachesOutsideHome(t *testing.T) {
	repoDockerfile := filepath.Join("..", "..", "Dockerfile")
	if _, err := os.Stat(repoDockerfile); err != nil {
		t.Skipf("repo Dockerfile not available: %v", err)
	}
	rendered, err := renderDockerfile(repoDockerfile, []string{"claude"}, []featureInstall{
		{Name: "devtools", Priority: 40, HasApt: true, HasScript: true},
		{Name: "user-cmds", Priority: 50, HasInstallCommands: true},
	}, nil, nil)
	if err != nil {
		t.Fatalf("renderDockerfile: %v", err)
	}

	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "type=cache") && strings.Contains(line, "target=/home/") {
			t.Fatalf("cache mount under the agent home:\n%s", line)
		}
	}
	for _, want := range []string{
		"--mount=type=cache,id=enclave-npm-${USER_ID},target=/var/cache/enclave/npm,uid=${USER_ID},gid=${GROUP_ID}",
		"--mount=type=cache,id=enclave-gomod-${USER_ID},target=/var/cache/enclave/gomod,uid=${USER_ID},gid=${GROUP_ID}",
		"--mount=type=cache,id=enclave-uv-${USER_ID},target=/var/cache/enclave/uv,uid=${USER_ID},gid=${GROUP_ID}",
		"npm_config_cache=/var/cache/enclave/npm GOMODCACHE=/var/cache/enclave/gomod UV_CACHE_DIR=/var/cache/enclave/uv",
		"--mount=type=cache,id=enclave-npm-${USER_ID}-claude,target=/var/cache/enclave/npm,",
		"npm_config_cache=/var/cache/enclave/npm enclave-install-tool claude",
		"mkdir -p /var/cache/enclave/npm /var/cache/enclave/gomod /var/cache/enclave/uv",
		"--mount=type=cache,id=enclave-apt-cache,target=/var/cache/apt",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Dockerfile lacks %q:\n%s", want, rendered)
		}
	}
	// The cache paths must stay RUN-scoped: the mounts do not persist into the
	// image, so an ENV pointing at them would break the tools at runtime.
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, "ENV ") && strings.Contains(line, "/var/cache/enclave") {
			t.Fatalf("cache path leaked into the image environment:\n%s", line)
		}
	}
}

func TestRenderEngineDockerfileIsIdenticalForDockerAndPodman(t *testing.T) {
	repoDockerfile := filepath.Join("..", "..", "Dockerfile")
	if _, err := os.Stat(repoDockerfile); err != nil {
		t.Skipf("repo Dockerfile not available: %v", err)
	}
	previous := docker.Binary()
	t.Cleanup(func() { docker.SetBinary(previous) })

	docker.SetBinary(backend.NameDocker)
	forDocker, err := renderEngineDockerfile(repoDockerfile, []string{"claude"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("renderEngineDockerfile (docker): %v", err)
	}
	docker.SetBinary(backend.NamePodman)
	forPodman, err := renderEngineDockerfile(repoDockerfile, []string{"claude"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("renderEngineDockerfile (podman): %v", err)
	}
	if forDocker != forPodman {
		t.Fatal("the rendered Dockerfile must not depend on the container engine")
	}
	if !strings.Contains(forPodman, "target=/var/cache/enclave/npm") {
		t.Fatal("podman builds must keep the package cache mounts")
	}
}
