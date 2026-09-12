// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// stubCLI installs an executable named name on a fresh PATH directory that
// prints version when asked for --version.
func stubCLI(t *testing.T, dir string, name string, version string) {
	t.Helper()
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '" + version + "'; fi\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil { // #nosec G306 -- test stub must be executable.
		t.Fatalf("write stub %s: %v", name, err)
	}
}

func TestDetectCLIs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		docker string
		podman bool
		want   []string
	}{
		{name: "none", want: nil},
		{name: "docker only", docker: "Docker version 27.1.1, build 6312585", want: []string{"docker"}},
		{name: "podman only", podman: true, want: []string{"podman"}},
		{name: "both", docker: "Docker version 27.1.1, build 6312585", podman: true, want: []string{"docker", "podman"}},
		{name: "podman-docker shim", docker: "podman version 5.8.1", podman: true, want: []string{"podman"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			if tc.docker != "" {
				stubCLI(t, dir, "docker", tc.docker)
			}
			if tc.podman {
				stubCLI(t, dir, "podman", "podman version 5.8.1")
			}
			if got := DetectCLIs(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DetectCLIs() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDockerIsPodmanShim(t *testing.T) {
	for _, tc := range []struct {
		name   string
		docker string
		want   bool
	}{
		{name: "no docker", want: false},
		{name: "real docker", docker: "Docker version 27.1.1, build 6312585", want: false},
		{name: "podman-docker shim", docker: "podman version 5.8.1", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			if tc.docker != "" {
				stubCLI(t, dir, "docker", tc.docker)
			}
			if got := DockerIsPodmanShim(); got != tc.want {
				t.Fatalf("DockerIsPodmanShim() = %v, want %v", got, tc.want)
			}
		})
	}
}
