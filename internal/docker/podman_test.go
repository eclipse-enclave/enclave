// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"slices"
	"testing"
)

func usePodmanBinary(t *testing.T) {
	t.Helper()
	previous := dockerBinary
	SetBinary("/usr/bin/podman")
	t.Cleanup(func() { dockerBinary = previous })
}

func TestIsPodmanMatchesBinaryBaseName(t *testing.T) {
	if IsPodman() {
		t.Fatal("default binary should be docker")
	}
	usePodmanBinary(t)
	if !IsPodman() {
		t.Fatalf("IsPodman() = false for %q", Binary())
	}
}

func TestBuildRunArgsPassesUserNamespaceMode(t *testing.T) {
	args := buildRunArgs(&ContainerConfig{Image: "example:test"}, &HostConfig{UserNS: "keep-id"}, "", runMode{})
	idx := slices.Index(args, "--userns")
	if idx < 0 || idx+1 >= len(args) || args[idx+1] != "keep-id" {
		t.Fatalf("expected --userns keep-id in %v", args)
	}
	if slices.Contains(buildRunArgs(&ContainerConfig{Image: "example:test"}, &HostConfig{}, "", runMode{}), "--userns") {
		t.Fatal("empty UserNS must not emit --userns")
	}
}

func TestDecodePodmanInfoMapsRootlessAndGraphRoot(t *testing.T) {
	info, err := decodePodmanInfo([]byte(`{"host":{"security":{"rootless":true}},"store":{"graphRoot":"/home/u/.local/share/containers/storage"}}`))
	if err != nil {
		t.Fatalf("decodePodmanInfo: %v", err)
	}
	if info.DockerRootDir != "/home/u/.local/share/containers/storage" {
		t.Fatalf("DockerRootDir = %q", info.DockerRootDir)
	}
	if !slices.Contains(info.SecurityOptions, "name=rootless") {
		t.Fatalf("expected rootless security option, got %v", info.SecurityOptions)
	}

	rootful, err := decodePodmanInfo([]byte(`{"host":{"security":{"rootless":false}},"store":{"graphRoot":"/var/lib/containers/storage"}}`))
	if err != nil {
		t.Fatalf("decodePodmanInfo: %v", err)
	}
	if len(rootful.SecurityOptions) != 0 {
		t.Fatalf("rootful podman must not report rootless, got %v", rootful.SecurityOptions)
	}
}

// The network backend version decides whether the per-session network may ask
// for isolate=strict, so it has to survive the podman info decode.
func TestDecodePodmanInfoMapsNetworkBackend(t *testing.T) {
	info, err := decodePodmanInfo([]byte(`{"host":{"networkBackend":"netavark","networkBackendInfo":{"backend":"netavark","version":"netavark 1.4.0"}},"store":{"graphRoot":"/var/lib/containers/storage"}}`))
	if err != nil {
		t.Fatalf("decodePodmanInfo: %v", err)
	}
	if info.NetworkBackend != "netavark" {
		t.Fatalf("NetworkBackend = %q, want netavark", info.NetworkBackend)
	}
	if info.NetworkBackendVersion != "netavark 1.4.0" {
		t.Fatalf("NetworkBackendVersion = %q, want \"netavark 1.4.0\"", info.NetworkBackendVersion)
	}

	// Older podman reports the backend only under networkBackendInfo.
	legacy, err := decodePodmanInfo([]byte(`{"host":{"networkBackendInfo":{"backend":"cni"}},"store":{"graphRoot":"/var/lib/containers/storage"}}`))
	if err != nil {
		t.Fatalf("decodePodmanInfo: %v", err)
	}
	if legacy.NetworkBackend != "cni" {
		t.Fatalf("NetworkBackend = %q, want cni", legacy.NetworkBackend)
	}
}

func TestBuildkitDisabledUnderPodman(t *testing.T) {
	t.Setenv("DOCKER_BUILDKIT", "")
	if !BuildkitEnabled() {
		t.Fatal("BuildKit should default to enabled for docker")
	}
	usePodmanBinary(t)
	if BuildkitEnabled() {
		t.Fatal("BuildKit flags must be suppressed for podman")
	}
}
