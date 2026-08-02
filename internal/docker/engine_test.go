// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// asEngine selects the named engine for the test and restores the docker
// default (including the stubbed binary and cached info) afterwards.
func asEngine(t *testing.T, name EngineName) {
	t.Helper()
	origBinary := dockerBinary
	SetEngine(name)
	t.Cleanup(func() {
		SetEngine(EngineDocker)
		dockerBinary = origBinary
		resetCachedInfo()
	})
}

// stubPodmanInfo points the CLI at a script answering `podman info` with the
// given schema fragment and clears the memoized info.
func stubPodmanInfo(t *testing.T, rootless bool, graphRoot string) {
	t.Helper()
	asEngine(t, EnginePodman)
	stub := filepath.Join(t.TempDir(), "podman")
	rootlessJSON := "false"
	if rootless {
		rootlessJSON = "true"
	}
	script := `#!/bin/sh
if [ "$1" = "info" ]; then
  printf '{"host":{"security":{"rootless":` + rootlessJSON + `}},"store":{"graphRoot":"` + graphRoot + `"}}\n'
  exit 0
fi
exit 2
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write podman stub: %v", err)
	}
	dockerBinary = stub
	resetCachedInfo()
}

func TestPodmanInfoMapsToSystemInfo(t *testing.T) {
	stubPodmanInfo(t, true, "/home/user/.local/share/containers/storage")
	info, err := Info(context.Background())
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if !info.HasSecurityOption("rootless") {
		t.Fatalf("expected rootless security option to be synthesized")
	}
	if info.DockerRootDir != "/home/user/.local/share/containers/storage" {
		t.Fatalf("expected graphRoot to map to DockerRootDir, got %q", info.DockerRootDir)
	}
}

func TestIsRootlessDetectsRootlessPodman(t *testing.T) {
	stubPodmanInfo(t, true, "/tmp/storage")
	rootless, err := IsRootless(context.Background())
	if err != nil {
		t.Fatalf("is rootless: %v", err)
	}
	if !rootless {
		t.Fatalf("expected rootless podman to be detected")
	}
}

func TestIsRootlessRootfulPodman(t *testing.T) {
	stubPodmanInfo(t, false, "/var/lib/containers/storage")
	rootless, err := IsRootless(context.Background())
	if err != nil {
		t.Fatalf("is rootless: %v", err)
	}
	if rootless {
		t.Fatalf("expected rootful podman not to report rootless")
	}
}

func TestSetEngineResetsCachedInfoOnChange(t *testing.T) {
	stubPodmanInfo(t, true, "/tmp/storage")
	if _, err := CachedInfo(context.Background()); err != nil {
		t.Fatalf("cached info: %v", err)
	}
	cachedInfoMu.Lock()
	loaded := cachedInfoLoaded
	cachedInfoMu.Unlock()
	if !loaded {
		t.Fatalf("expected info to be cached")
	}
	SetEngine(EnginePodman) // same engine: cache kept
	cachedInfoMu.Lock()
	loaded = cachedInfoLoaded
	cachedInfoMu.Unlock()
	if !loaded {
		t.Fatalf("expected same-engine SetEngine to keep the cache")
	}
	SetEngine(EngineDocker) // engine switch: cache dropped
	cachedInfoMu.Lock()
	loaded = cachedInfoLoaded
	cachedInfoMu.Unlock()
	if loaded {
		t.Fatalf("expected engine switch to reset the cache")
	}
}

func TestExactNameFilterPerEngine(t *testing.T) {
	if got := ExactNameFilter("enclave-claude-abc"); got != "^/enclave-claude-abc$" {
		t.Fatalf("docker filter = %q", got)
	}
	asEngine(t, EnginePodman)
	if got := ExactNameFilter("enclave-claude-abc"); got != "^enclave-claude-abc$" {
		t.Fatalf("podman filter = %q", got)
	}
}

func TestIsNotFoundPodmanPhrasings(t *testing.T) {
	for _, stderr := range []string{
		`Error: nosuchimage:latest: image not known`,
		`Error: network nosuchnet: unable to find network with name or ID nosuchnet: network not found`,
		`Error: no container with name or ID "x" found: no such container`,
	} {
		if !IsNotFound(&cliError{stderr: stderr}) {
			t.Fatalf("expected not-found classification for %q", stderr)
		}
	}
}

func TestBuildRunArgsUserns(t *testing.T) {
	args := buildRunArgs(&ContainerConfig{Image: "img"}, &HostConfig{UsernsMode: "keep-id"}, "n", runMode{})
	found := false
	for i, arg := range args {
		if arg == "--userns" && i+1 < len(args) && args[i+1] == "keep-id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected --userns keep-id in %v", args)
	}
}

// Podman 4.x renders Config.Entrypoint as a bare string; docker renders an
// array. Both must decode, or listing silently drops the container.
func TestDecodeInspectResponseStringEntrypoint(t *testing.T) {
	for _, line := range []string{
		`{"Id":"abc","Name":"x","Config":{"Entrypoint":"/entry.sh","Cmd":["run"],"Labels":{"enclave.agent":"claude"}}}`,
		`{"Id":"def","Name":"/y","Config":{"Entrypoint":["/entry.sh"],"Cmd":null}}`,
	} {
		results := decodeInspectResponses(line)
		if len(results) != 1 {
			t.Fatalf("expected 1 decoded entry for %q, got %d", line, len(results))
		}
		if got := results[0].Config.Entrypoint; len(got) != 1 || got[0] != "/entry.sh" {
			t.Fatalf("entrypoint = %v", got)
		}
	}
}

func TestBuildkitDisabledForPodman(t *testing.T) {
	asEngine(t, EnginePodman)
	if BuildkitEnabled() {
		t.Fatalf("expected BuildkitEnabled to be false under podman")
	}
}
