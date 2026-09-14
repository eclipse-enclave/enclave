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

// stubBuildAttempts replaces the engine build with a scripted sequence of
// attempts, each writing output and returning an error, and records the
// network mode of every request.
func stubBuildAttempts(t *testing.T, attempts []struct {
	output string
	err    error
}) *[]string {
	t.Helper()
	var modes []string
	call := 0
	orig := dockerBuildImage
	dockerBuildImage = func(_ context.Context, req docker.BuildRequest, out io.Writer) error {
		if call >= len(attempts) {
			t.Fatalf("unexpected build attempt %d", call+1)
		}
		attempt := attempts[call]
		call++
		modes = append(modes, req.NetworkMode)
		_, _ = io.WriteString(out, attempt.output)
		return attempt.err
	}
	t.Cleanup(func() { dockerBuildImage = orig })
	return &modes
}

type buildAttempt = struct {
	output string
	err    error
}

func TestRunImageBuildRetriesWithHostNetworkOnDockerDNSFailure(t *testing.T) {
	withContainerCLI(t, backend.NameDocker)
	modes := stubBuildAttempts(t, []buildAttempt{
		{output: "Err:1 http://deb.debian.org/debian trixie InRelease\n  Temporary failure resolving 'deb.debian.org'\n", err: errors.New("exit status 100")},
		{output: "Successfully built\n"},
	})

	var shown strings.Builder
	if err := runImageBuild(context.Background(), docker.BuildRequest{}, &shown); err != nil {
		t.Fatalf("expected the host-network retry to succeed, got %v", err)
	}
	if got := *modes; len(got) != 2 || got[0] != "" || got[1] != "host" {
		t.Fatalf("expected default then host network, got %v", got)
	}
	if !strings.Contains(shown.String(), "Temporary failure resolving") || !strings.Contains(shown.String(), "Successfully built") {
		t.Fatalf("build output must still stream to the caller, got %q", shown.String())
	}
}

func TestRunImageBuildDoesNotRetryOnPodman(t *testing.T) {
	withContainerCLI(t, backend.NamePodman)
	modes := stubBuildAttempts(t, []buildAttempt{
		{output: "Temporary failure resolving 'deb.debian.org'\n", err: errors.New("exit status 100")},
	})

	err := runImageBuild(context.Background(), docker.BuildRequest{}, io.Discard)
	if err == nil {
		t.Fatal("expected the build failure to be reported")
	}
	if len(*modes) != 1 {
		t.Fatalf("podman must not retry with host networking, got %d attempts", len(*modes))
	}
	if !strings.Contains(err.Error(), "network transfer failed or timed out") {
		t.Fatalf("expected the connectivity hint in %q", err.Error())
	}
	if strings.Contains(err.Error(), "host build network") {
		t.Fatalf("error must not mention a retry that did not happen: %q", err.Error())
	}
}

func TestRunImageBuildDoesNotRetryNonDNSFailures(t *testing.T) {
	withContainerCLI(t, backend.NameDocker)
	modes := stubBuildAttempts(t, []buildAttempt{
		{output: "ERROR: process \"/bin/sh -c exit 1\" did not complete successfully: exit code: 1\n", err: errors.New("exit status 1")},
	})

	err := runImageBuild(context.Background(), docker.BuildRequest{}, io.Discard)
	if err == nil {
		t.Fatal("expected the build failure to be reported")
	}
	if len(*modes) != 1 {
		t.Fatalf("a non-DNS failure must not trigger the host-network retry, got %d attempts", len(*modes))
	}
	if !strings.Contains(err.Error(), "failed to build image: exit status 1") {
		t.Fatalf("expected the engine error to be wrapped directly, got %q", err.Error())
	}
}

func TestRunImageBuildNamesTransientNetworkFailures(t *testing.T) {
	withContainerCLI(t, backend.NameDocker)
	stubBuildAttempts(t, []buildAttempt{
		{output: "read tcp 10.0.2.100:44210->151.101.1.6:443: read: connection reset by peer\n", err: errors.New("exit status 1")},
	})

	err := runImageBuild(context.Background(), docker.BuildRequest{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "network transfer failed or timed out") {
		t.Fatalf("expected the connectivity hint, got %v", err)
	}
}

func TestRunImageBuildReportsFailedHostNetworkRetry(t *testing.T) {
	withContainerCLI(t, backend.NameDocker)
	modes := stubBuildAttempts(t, []buildAttempt{
		{output: "Temporary failure resolving 'deb.debian.org'\n", err: errors.New("exit status 100")},
		{output: "curl: (28) Operation timed out after 60001 milliseconds\n", err: errors.New("exit status 28")},
	})

	err := runImageBuild(context.Background(), docker.BuildRequest{}, io.Discard)
	if err == nil {
		t.Fatal("expected the retried build failure to be reported")
	}
	if len(*modes) != 2 {
		t.Fatalf("expected one retry, got %d attempts", len(*modes))
	}
	if !strings.Contains(err.Error(), "exit status 28") || !strings.Contains(err.Error(), "network transfer failed or timed out") {
		t.Fatalf("expected the retry's error and the connectivity hint, got %q", err.Error())
	}
}
