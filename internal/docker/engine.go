// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// DetectCLIs returns the supported container CLIs found on PATH, docker
// first. A docker executable that is really the podman-docker shim is
// reported as podman: driving it as Docker would skip the user-namespace
// handling rootless podman needs.
func DetectCLIs() []string {
	var found []string
	if path, err := exec.LookPath("docker"); err == nil {
		if isPodmanShim(path) {
			found = append(found, podmanBinary)
		} else {
			found = append(found, "docker")
		}
	}
	if _, err := exec.LookPath(podmanBinary); err == nil && !slices.Contains(found, podmanBinary) {
		found = append(found, podmanBinary)
	}
	return found
}

// isPodmanShim reports whether the docker executable at path identifies
// itself as podman. `--version` is answered by the client alone, so this
// works without a running daemon.
func isPodmanShim(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output() // #nosec G204 -- path is the docker executable resolved from PATH.
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), podmanBinary)
}
