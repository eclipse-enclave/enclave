// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/docker"
)

// shortBuildRetryDelay removes the pause before a rerun for the test.
func shortBuildRetryDelay(t *testing.T) {
	t.Helper()
	orig := docker.BuildRetryDelay
	docker.BuildRetryDelay = 0
	t.Cleanup(func() { docker.BuildRetryDelay = orig })
}

// stubBuildOutcome replaces the engine build with one that writes output and
// returns err, recording every request it receives.
func stubBuildOutcome(t *testing.T, output string, err error) *[]docker.BuildRequest {
	t.Helper()
	var requests []docker.BuildRequest
	orig := dockerBuildImage
	dockerBuildImage = func(_ context.Context, req docker.BuildRequest, out io.Writer) error {
		requests = append(requests, req)
		_, _ = io.WriteString(out, output)
		return err
	}
	t.Cleanup(func() { dockerBuildImage = orig })
	return &requests
}

func TestRunImageBuildNeverRetriesOnHostNetwork(t *testing.T) {
	// Build output is partly written by extension install scripts, so a DNS
	// error in it must not move the build onto the host network by itself. The
	// transient-failure rerun keeps the requested network mode.
	withContainerCLI(t, backend.NameDocker)
	t.Setenv(docker.BuildRetriesEnv, "1")
	shortBuildRetryDelay(t)
	requests := stubBuildOutcome(t, "Err:1 http://deb.debian.org/debian trixie InRelease\n  Temporary failure resolving 'deb.debian.org'\n", errors.New("exit status 100"))

	var shown strings.Builder
	err := runImageBuild(context.Background(), docker.BuildRequest{}, &shown)
	if err == nil {
		t.Fatal("expected the build failure to be reported")
	}
	if len(*requests) != 2 {
		t.Fatalf("expected the original attempt and one rerun, got %d", len(*requests))
	}
	for _, req := range *requests {
		if req.NetworkMode != "" {
			t.Fatalf("no attempt may switch to another network mode, got %q", req.NetworkMode)
		}
	}
	if !strings.Contains(err.Error(), docker.BuildNetworkEnv+"=host") {
		t.Fatalf("a Docker DNS failure must point at the explicit override, got %q", err.Error())
	}
	if !strings.Contains(shown.String(), "Temporary failure resolving") {
		t.Fatalf("build output must still stream to the caller, got %q", shown.String())
	}
}

func TestRunImageBuildDNSHintOnlyOnDockerDefaultNetwork(t *testing.T) {
	t.Setenv(docker.BuildRetriesEnv, "0")
	dnsFailure := "Temporary failure resolving 'deb.debian.org'\n"

	withContainerCLI(t, backend.NamePodman)
	stubBuildOutcome(t, dnsFailure, errors.New("exit status 100"))
	err := runImageBuild(context.Background(), docker.BuildRequest{}, io.Discard)
	if err == nil || strings.Contains(err.Error(), docker.BuildNetworkEnv) {
		t.Fatalf("podman has no build-network DNS quirk; expected a plain connectivity hint, got %v", err)
	}
	if !strings.Contains(err.Error(), "network transfer failed or timed out") {
		t.Fatalf("expected the connectivity hint, got %v", err)
	}

	withContainerCLI(t, backend.NameDocker)
	stubBuildOutcome(t, dnsFailure, errors.New("exit status 100"))
	err = runImageBuild(context.Background(), docker.BuildRequest{NetworkMode: "host"}, io.Discard)
	if err == nil || strings.Contains(err.Error(), docker.BuildNetworkEnv) {
		t.Fatalf("already on the host network; the override hint would be misleading, got %v", err)
	}
}

func TestRunImageBuildWrapsGenericFailuresDirectly(t *testing.T) {
	t.Setenv(docker.BuildRetriesEnv, "0")
	withContainerCLI(t, backend.NameDocker)
	stubBuildOutcome(t, "ERROR: process \"/bin/sh -c exit 1\" did not complete successfully: exit code: 1\n", errors.New("exit status 1"))

	err := runImageBuild(context.Background(), docker.BuildRequest{}, io.Discard)
	if err == nil || err.Error() != "failed to build image: exit status 1" {
		t.Fatalf("expected the engine error to be wrapped directly, got %v", err)
	}
}

func TestRunImageBuildNamesTransientNetworkFailures(t *testing.T) {
	t.Setenv(docker.BuildRetriesEnv, "0")
	withContainerCLI(t, backend.NameDocker)
	stubBuildOutcome(t, "read tcp 10.0.2.100:44210->151.101.1.6:443: read: connection reset by peer\n", errors.New("exit status 1"))

	err := runImageBuild(context.Background(), docker.BuildRequest{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "network transfer failed or timed out") {
		t.Fatalf("expected the connectivity hint, got %v", err)
	}
}

func TestRunImageBuildSucceedsQuietly(t *testing.T) {
	t.Setenv(docker.BuildRetriesEnv, "0")
	withContainerCLI(t, backend.NameDocker)
	requests := stubBuildOutcome(t, "", nil)
	if err := runImageBuild(context.Background(), docker.BuildRequest{Progress: "quiet"}, io.Discard); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if got := (*requests)[0].Progress; got != "quiet" {
		t.Fatalf("request must pass through unchanged, got progress %q", got)
	}
}
