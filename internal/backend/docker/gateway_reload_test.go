// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"reflect"
	"testing"
	"time"

	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
)

func verifyMountForTest(id string, expected string) error {
	b := New(Options{})
	return b.VerifyGatewayConfigMount(context.Background(), id, expected)
}

func TestParseGatewayTargetsSorted(t *testing.T) {
	containers := []dockercmd.Summary{
		{
			ID:    "bbbbbbbbbbbbbbbb",
			Names: []string{"/zeta"},
			Labels: map[string]string{
				model.GatewayLabelAgent:       "codex",
				model.GatewayLabelProjectHash: "p2",
				model.GatewayLabelContainer:   "parent-b",
			},
		},
		{
			ID:    "aaaaaaaaaaaaaaaa",
			Names: []string{"/beta"},
			Labels: map[string]string{
				model.GatewayLabelAgent:       "codex",
				model.GatewayLabelProjectHash: "p1",
				model.GatewayLabelContainer:   "parent-a",
			},
		},
		{
			ID:    "cccccccccccccccc",
			Names: []string{"/alpha"},
			Labels: map[string]string{
				model.GatewayLabelAgent:       "claude",
				model.GatewayLabelProjectHash: "p1",
				model.GatewayLabelContainer:   "parent-c",
			},
		},
	}

	targets := gatewayInfosFromSummaries(containers)
	var names []string
	for _, target := range targets {
		names = append(names, target.Name)
	}

	want := []string{"alpha", "beta", "zeta"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("unexpected order: want %v, got %v", want, names)
	}
}

func TestContainerDisplayNameFallsBackToShortID(t *testing.T) {
	name := containerDisplayName(nil, "1234567890abcdef")
	if name != "1234567890ab" {
		t.Fatalf("expected short ID, got %q", name)
	}
}

func TestParseGatewayTargetsIncludesProjectDirLabel(t *testing.T) {
	containers := []dockercmd.Summary{
		{
			ID:    "aaaaaaaaaaaaaaaa",
			Names: []string{"/alpha"},
			Labels: map[string]string{
				model.GatewayLabelAgent:       "codex",
				model.GatewayLabelProjectHash: "p1",
				model.GatewayLabelProjectDir:  "/work/project",
				model.GatewayLabelContainer:   "parent-a",
			},
		},
	}
	targets := gatewayInfosFromSummaries(containers)
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].ProjectDir != "/work/project" {
		t.Fatalf("expected project dir label to be captured, got %q", targets[0].ProjectDir)
	}
}

func TestVerifyGatewayBundleMountRejectsMismatchedSource(t *testing.T) {
	origInspect := gatewayContainerInspect
	gatewayContainerInspect = func(context.Context, string) (dockercmd.InspectResponse, error) {
		return dockercmd.InspectResponse{
			Mounts: []dockercmd.MountPoint{{
				Source:      "/wrong/source",
				Destination: model.GatewayConfigDir,
			}},
		}, nil
	}
	defer func() { gatewayContainerInspect = origInspect }()

	err := verifyMountForTest("abc123", "/expected/source")
	if err == nil || err.Error() != "gateway config mount source mismatch (expected /expected/source)" {
		t.Fatalf("verifyGatewayBundleMount() error = %v, want mismatch error", err)
	}
}

func TestWaitForGatewayReloadFailsOnFailureMarker(t *testing.T) {
	origLogs := gatewayContainerLogsSince
	gatewayContainerLogsSince = func(context.Context, string, time.Time) (string, error) {
		return "[enclave-gateway] Reload validation failed (generation=gen-1)\n", nil
	}
	defer func() { gatewayContainerLogsSince = origLogs }()

	err := waitForGatewayReload(context.Background(), "abc123", "gen-1", time.Now())
	if err == nil || err.Error() != "gateway reload failed (Reload validation failed (generation=gen-1))" {
		t.Fatalf("waitForGatewayReload() error = %v, want failure marker error", err)
	}
}

func TestWaitForGatewayReloadTimesOut(t *testing.T) {
	origLogs := gatewayContainerLogsSince
	origInspect := gatewayContainerInspect
	origTimeout := gatewayReloadConfirmTimeout
	origInterval := gatewayReloadPollInterval
	gatewayReloadConfirmTimeout = 5 * time.Millisecond
	gatewayReloadPollInterval = 1 * time.Millisecond
	gatewayContainerLogsSince = func(context.Context, string, time.Time) (string, error) {
		return "", nil
	}
	gatewayContainerInspect = func(context.Context, string) (dockercmd.InspectResponse, error) {
		return dockercmd.InspectResponse{
			State: &dockercmd.ContainerState{Running: true},
		}, nil
	}
	defer func() {
		gatewayContainerLogsSince = origLogs
		gatewayContainerInspect = origInspect
		gatewayReloadConfirmTimeout = origTimeout
		gatewayReloadPollInterval = origInterval
	}()

	err := waitForGatewayReload(context.Background(), "abc123", "gen-1", time.Now())
	if err == nil {
		t.Fatal("waitForGatewayReload() error = nil, want timeout")
	}
	if got := err.Error(); got != "timed out waiting for gateway reload confirmation (generation=gen-1)" {
		t.Fatalf("waitForGatewayReload() error = %q, want timeout message", got)
	}
}
