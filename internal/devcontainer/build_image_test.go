// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package devcontainer

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"enclave/internal/docker"
)

func stubDevcontainerBuild(t *testing.T, output string, err error) *[]docker.BuildRequest {
	t.Helper()
	var requests []docker.BuildRequest
	orig := dockerBuild
	dockerBuild = func(_ context.Context, req docker.BuildRequest, out io.Writer) error {
		requests = append(requests, req)
		_, _ = io.WriteString(out, output)
		return err
	}
	t.Cleanup(func() { dockerBuild = orig })
	return &requests
}

func testBuildSpec() Spec {
	return Spec{BaseImage: "enclave-devcontainer-base:test", DockerfilePath: "Dockerfile", ContextDir: "."}
}

func TestBuildImagePropagatesBuildNetworkOverride(t *testing.T) {
	t.Setenv(docker.BuildNetworkEnv, "host")
	requests := stubDevcontainerBuild(t, "", nil)
	if err := BuildImage(testBuildSpec()); err != nil {
		t.Fatalf("BuildImage: %v", err)
	}
	if len(*requests) != 1 || (*requests)[0].NetworkMode != "host" {
		t.Fatalf("expected one build on the host network, got %+v", *requests)
	}
}

func TestBuildImageRejectsInvalidBuildNetwork(t *testing.T) {
	t.Setenv(docker.BuildNetworkEnv, "bridge")
	requests := stubDevcontainerBuild(t, "", nil)
	if err := BuildImage(testBuildSpec()); err == nil || !strings.Contains(err.Error(), docker.BuildNetworkEnv) {
		t.Fatalf("expected the invalid override to be rejected, got %v", err)
	}
	if len(*requests) != 0 {
		t.Fatal("no build must run with an invalid network override")
	}
}

func TestBuildImagePointsAtOverrideOnDockerDNSFailure(t *testing.T) {
	t.Setenv(docker.BuildNetworkEnv, "")
	t.Setenv(docker.BuildRetriesEnv, "0")
	previous := docker.Binary()
	docker.SetBinary("docker")
	t.Cleanup(func() { docker.SetBinary(previous) })
	stubDevcontainerBuild(t, "Temporary failure resolving 'deb.debian.org'\n", errors.New("exit status 100"))

	err := BuildImage(testBuildSpec())
	if err == nil || !strings.Contains(err.Error(), docker.BuildNetworkEnv+"=host") {
		t.Fatalf("expected the override hint, got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "failed to build devcontainer base image: ") {
		t.Fatalf("expected the devcontainer prefix, got %q", err.Error())
	}
}
