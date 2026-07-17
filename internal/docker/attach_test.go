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

func TestAttachConnectFailed(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{"clean detach or container exit (no docker error)", "", false},
		{"container log output is not a failure", "app: starting\napp: done", false},
		{"no such container", "Error: No such container: abc123", true},
		{"stopped container", "You cannot attach to a stopped container, start it first", true},
		{"not running", "Error response from daemon: container abc is not running", true},
		{"invalid detach keys", "Error: invalid detach keys (ctrl-zz) provided", true},
		{"daemon unreachable", "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", true},
		{"permission denied", "permission denied while trying to connect to the Docker daemon socket", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := attachConnectFailed(tt.stderr); got != tt.want {
				t.Errorf("attachConnectFailed(%q) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}

// TestAttachInteractiveContract drives AttachInteractive against a stubbed
// docker binary to lock the contract: connect failures surface as errors while
// a detach or non-zero container exit (e.g. Ctrl-C → 130) returns nil.
func TestAttachInteractiveContract(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "docker")
	// #nosec G306 -- a stub executable for the test must carry the exec bit.
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s' \"$STUB_STDERR\" >&2\nexit \"$STUB_EXIT\"\n"), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	orig := dockerBinary
	dockerBinary = stub
	t.Cleanup(func() { dockerBinary = orig })

	t.Run("connect failure surfaces error", func(t *testing.T) {
		t.Setenv("STUB_STDERR", "Error: No such container: nope")
		t.Setenv("STUB_EXIT", "1")
		if err := AttachInteractive(context.Background(), "nope", ""); err == nil {
			t.Fatal("expected error when docker could not attach, got nil")
		}
	})

	t.Run("container exit code is benign", func(t *testing.T) {
		t.Setenv("STUB_STDERR", "")
		t.Setenv("STUB_EXIT", "130") // e.g. Ctrl-C forwarded to the container
		if err := AttachInteractive(context.Background(), "running", ""); err != nil {
			t.Fatalf("expected nil for container exit/detach, got %v", err)
		}
	})
}
