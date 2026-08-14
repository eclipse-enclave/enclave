// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"path/filepath"

	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	backendqemu "enclave/internal/backend/qemu"
	"enclave/internal/model"
)

func dockerBackendOptions(host model.Host, paths model.Paths, build model.BuildOptions, run model.RunOptions) backenddocker.Options {
	reconcileScriptPath := ""
	if paths.AppRoot != "" {
		reconcileScriptPath = filepath.Join(paths.AppRoot, "runtime-assets", "auth-reconcile.sh")
	}
	return backenddocker.Options{
		Host:                host,
		Paths:               paths,
		ReconcileScriptPath: reconcileScriptPath,
		ForceRebuild:        build.ForceRebuild,
		NoRebuild:           build.NoRebuild,
		NetworkLogMode:      run.NetworkLog,
		ProjectMount:        run.ProjectMount,
	}
}

func qemuBackendOptions(host model.Host, paths model.Paths) backendqemu.Options {
	return backendqemu.Options{Host: host, Paths: paths}
}

// newListingBackend builds a backend for read-only commands (ps/status/theia/
// img import) that only enumerate existing sessions and therefore need no
// resolved host or paths.
func newListingBackend(opts model.Options) (backend.Backend, error) {
	return selectBackend(opts, dockerBackendOptions(model.Host{}, model.Paths{}, opts.BuildOptions, opts.RunOptions))
}

func selectBackend(opts model.Options, dockerOpts backenddocker.Options) (backend.Backend, error) {
	name := opts.Backend
	if name == "" {
		name = backend.NameDocker
	}
	switch name {
	case backend.NameDocker:
		return backenddocker.New(dockerOpts), nil
	case backend.NameQEMU:
		return backendqemu.New(qemuBackendOptions(dockerOpts.Host, dockerOpts.Paths)), nil
	default:
		return nil, fmt.Errorf("unsupported backend %q (available: %s, %s)", name, backend.NameDocker, backend.NameQEMU)
	}
}
