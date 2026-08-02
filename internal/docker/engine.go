// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

// EngineName identifies the container engine this package drives. Podman is
// close enough to Docker's CLI that both share this wrapper; the remaining
// divergences (info schema, error phrasings, build flags) branch on the
// selected engine.
type EngineName string

const (
	EngineDocker EngineName = "docker"
	EnginePodman EngineName = "podman"
)

var engineName = EngineDocker

// SetEngine selects the container engine binary this package shells out to.
// It must run before any command in this package. Switching engines resets
// the cached `info` result so a late switch cannot serve the previous
// engine's data; repeated calls with the same engine keep the cache.
func SetEngine(name EngineName) {
	if name == "" {
		name = EngineDocker
	}
	if name == engineName {
		return
	}
	engineName = name
	dockerBinary = string(name)
	resetCachedInfo()
}

// Engine returns the selected container engine.
func Engine() EngineName { return engineName }

// IsPodman reports whether the podman engine is selected.
func IsPodman() bool { return engineName == EnginePodman }
