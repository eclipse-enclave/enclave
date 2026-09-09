// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	backendqemu "enclave/internal/backend/qemu"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/prompt"
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
	case backend.NameDocker, backend.NamePodman:
		return backenddocker.New(dockerOpts), nil
	case backend.NameQEMU:
		return backendqemu.New(qemuBackendOptions(dockerOpts.Host, dockerOpts.Paths)), nil
	default:
		return nil, fmt.Errorf("unsupported backend %q (available: %s, %s, %s)", name, backend.NameDocker, backend.NamePodman, backend.NameQEMU)
	}
}

// Seams for backend resolution, replaced in tests.
var (
	detectContainerCLIs = backenddocker.DetectCLIs
	backendPromptUsable = func() bool {
		return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd())) // #nosec G115 -- file descriptors fit in int on all supported platforms.
	}
	saveBackendChoice = config.WriteGlobalDefault
)

// resolveBackend replaces the "auto" backend with the container engine this
// host has and points the shared CLI wrapper at it. It runs once, right after
// option resolution, because image builds and engine checks use the wrapper
// before any backend instance exists. Explicit backends are left untouched.
func resolveBackend(opts *model.Options) {
	if strings.TrimSpace(opts.Backend) == backend.NameAuto {
		opts.Backend = detectBackend()
	}
	switch opts.Backend {
	case backend.NameDocker, backend.NamePodman:
		backenddocker.UseCLI(opts.Backend)
	}
}

// detectBackend picks the engine for the "auto" backend. A single installed
// engine is used as is. With both installed, an interactive terminal is asked
// once and the answer is saved to the global config; without a terminal
// (scripts, --json consumers) docker is used with a notice instead of
// blocking on a prompt. With no engine at all the docker default stays so the
// engine check reports what is missing.
func detectBackend() string {
	clis := detectContainerCLIs()
	switch len(clis) {
	case 0:
		return backend.NameDocker
	case 1:
		return clis[0]
	}
	configPath, err := config.GlobalConfigPath()
	if err != nil {
		configPath = "the global config.json"
	}
	if !backendPromptUsable() {
		logx.Warnf("Both docker and podman are installed; using docker. Set \"backend\" in %s to choose.", configPath)
		return backend.NameDocker
	}
	question := fmt.Sprintf("Both docker and podman are installed. Which should enclave use? Rootless podman keeps container root unprivileged on the host. The answer is saved to %s.", configPath)
	choice, err := prompt.Choose(question, []string{backend.NamePodman, backend.NameDocker}, os.Stdin, os.Stderr)
	if err != nil || choice == "" {
		logx.Warnf("No backend chosen; using docker for this run.")
		return backend.NameDocker
	}
	if _, err := saveBackendChoice("backend", choice); err != nil {
		logx.Warnf("Using %s for this run, but the choice could not be saved: %v", choice, err)
	}
	return choice
}
