// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"errors"
	"os/exec"
	"reflect"
	"testing"
)

func TestIsNotFound(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stderr string
		want   bool
	}{
		{name: "no such container", stderr: "Error: No such container: abc123", want: true},
		{name: "no such image", stderr: "Error: No such image: foo:latest", want: true},
		{name: "no such volume", stderr: "Error: No such volume: myvol", want: true},
		{name: "network not found", stderr: "Error response from daemon: network missing not found", want: true},
		{name: "no such object", stderr: "Error: No such object: abc123", want: true},
		// The reviewed footgun: a bare "not found" also appears in unrelated
		// failures and must not be classified as a missing-object error.
		{name: "executable not found", stderr: `OCI runtime create failed: exec: "foo": executable file not found in $PATH`, want: false},
		{name: "manifest not found", stderr: "manifest unknown: manifest not found", want: false},
		{name: "empty", stderr: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotFound(&cliError{stderr: tc.stderr}); got != tc.want {
				t.Fatalf("IsNotFound(%q) = %v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
}

func TestIsNotFoundOnlyClassifiesCLIErrors(t *testing.T) {
	if IsNotFound(nil) {
		t.Fatal("nil should not be classified as not-found")
	}
	// A plain error, even one that mentions "no such container", is not a
	// *cliError and so should not be classified.
	if IsNotFound(errors.New("no such container")) {
		t.Fatal("non-*cliError should not be classified as not-found")
	}
}

func TestIsSocketPermissionDenied(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stderr string
		want   bool
	}{
		// Older engines phrase it as "Docker daemon socket", newer ones as
		// "docker API"; both share the matched prefix.
		{name: "daemon socket phrasing", stderr: "permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock: Get ...: dial unix /var/run/docker.sock: connect: permission denied", want: true},
		{name: "docker api phrasing", stderr: "permission denied while trying to connect to the docker API at unix:///var/run/docker.sock", want: true},
		{name: "daemon down", stderr: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", want: false},
		// Unrelated permission errors (e.g. bind mounts) must not be
		// classified as socket-access failures.
		{name: "bind mount denied", stderr: "error while creating mount source path: mkdir /data: permission denied", want: false},
		{name: "empty", stderr: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSocketPermissionDenied(&cliError{stderr: tc.stderr}); got != tc.want {
				t.Fatalf("IsSocketPermissionDenied(%q) = %v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
	if IsSocketPermissionDenied(errors.New("permission denied while trying to connect")) {
		t.Fatal("non-*cliError should not be classified as socket permission denied")
	}
}

func TestNetworkErrorClassifiers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stderr string
		check  func(error) bool
	}{
		{name: "already exists", stderr: "network with name session-net already exists", check: IsAlreadyExists},
		{name: "container name conflict", stderr: `Conflict. The container name "/session-gateway" is already in use by container "abc".`, check: IsContainerNameConflict},
		{name: "active endpoints", stderr: "network session-net has active endpoints", check: IsActiveEndpoints},
		{name: "podman associated containers", stderr: `Error: "session-net" has associated containers with it. Use -f to forcibly delete containers and pods: network is being used`, check: IsActiveEndpoints},
		{name: "podman isolate parse", stderr: `Error: strconv.ParseBool: parsing "strict": invalid syntax`, check: IsUnsupportedIsolateValue},
		{name: "podman isolate unsupported", stderr: "Error: unsupported bridge network option isolate", check: IsUnsupportedIsolateValue},
		{name: "default pool exhausted", stderr: "could not find an available, non-overlapping IPv4 address pool among the defaults to assign to the network", check: IsAddressPoolExhausted},
		{name: "predefined pools exhausted", stderr: "all predefined address pools have been fully subnetted", check: IsAddressPoolExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.check(&cliError{stderr: tc.stderr}) {
				t.Fatalf("classifier rejected %q", tc.stderr)
			}
			if tc.check(errors.New(tc.stderr)) {
				t.Fatal("classifier accepted a non-CLI error")
			}
		})
	}
}

func TestIsCLIUnavailable(t *testing.T) {
	wrapped := &cliError{err: &exec.Error{Name: "docker", Err: exec.ErrNotFound}}
	if !IsCLIUnavailable(wrapped) {
		t.Fatal("exec.ErrNotFound wrapped in *cliError should classify as CLI unavailable")
	}
	if IsCLIUnavailable(&cliError{stderr: "Cannot connect to the Docker daemon"}) {
		t.Fatal("daemon connectivity failure should not classify as CLI unavailable")
	}
	if IsCLIUnavailable(nil) {
		t.Fatal("nil should not classify as CLI unavailable")
	}
}

func TestBuildRunArgsDetachedInteractive(t *testing.T) {
	got := buildRunArgs(&ContainerConfig{Image: "image"}, nil, "managed", runMode{Detach: true, Interactive: true, TTY: true})
	wantPrefix := []string{"run", "--detach", "--interactive", "--tty", "--name", "managed", "image"}
	if !reflect.DeepEqual(got, wantPrefix) {
		t.Fatalf("buildRunArgs() = %v, want %v", got, wantPrefix)
	}
}

func TestBuildRunArgsCreateMode(t *testing.T) {
	got := buildRunArgs(&ContainerConfig{Image: "image", Entrypoint: []string{"true"}}, &HostConfig{UserNS: "keep-id"}, "warmup", runMode{Create: true})
	want := []string{"create", "--name", "warmup", "--entrypoint", "true", "--userns", "keep-id", "image"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildRunArgs() = %v, want %v", got, want)
	}
}

// Regression for macOS cache deletion: disposable directory binds must use the
// source-creating `--volume` form, while every other mount keeps the strict
// `--mount` form so a missing required source stays an error.
func TestMountFlagsSourceCreatingBinds(t *testing.T) {
	got := mountFlags([]Mount{
		{Type: MountTypeBind, Source: "/host/project", Target: "/work"},
		{Type: MountTypeBind, Source: "/host/secret.json", Target: "/auth.json", ReadOnly: true},
		{Type: MountTypeBind, Source: "/host/cache/npm", Target: "/home/agent/.npm", CreateSourceDir: true},
		{Type: MountTypeBind, Source: "/host/cache/inbox", Target: "/mnt/host-images", ReadOnly: true, CreateSourceDir: true},
		{Type: MountTypeVolume, Source: "vol", Target: "/data", CreateSourceDir: true},
	})
	want := []string{
		"--mount", "type=bind,source=/host/project,target=/work",
		"--mount", "type=bind,source=/host/secret.json,target=/auth.json,readonly",
		"--volume", "/host/cache/npm:/home/agent/.npm",
		"--volume", "/host/cache/inbox:/mnt/host-images:ro",
		"--mount", "type=volume,source=vol,target=/data",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mountFlags() = %v, want %v", got, want)
	}
}
