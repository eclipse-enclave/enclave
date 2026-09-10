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
	"enclave/internal/cli"
	"enclave/internal/config"
	"enclave/internal/docker"
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
		dockerOpts.Engine = name
		return backenddocker.New(dockerOpts), nil
	case backend.NameQEMU:
		return backendqemu.New(qemuBackendOptions(dockerOpts.Host, dockerOpts.Paths)), nil
	default:
		return nil, fmt.Errorf("unsupported backend %q (available: %s, %s, %s)", name, backend.NameDocker, backend.NamePodman, backend.NameQEMU)
	}
}

// Seams for backend resolution, replaced in tests.
var (
	detectContainerCLIs = docker.DetectCLIs
	dockerIsPodmanShim  = docker.DockerIsPodmanShim
	backendPromptUsable = func() bool {
		// Redirected stdout means a consumer captures the output even while
		// stdin and stderr are terminals; result=$(enclave ps) must not block
		// on a question.
		return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stderr.Fd())) // #nosec G115 -- file descriptors fit in int on all supported platforms.
	}
	saveBackendChoice = config.WriteGlobalDefault
)

// backendFreeActions never touch a container engine, so the "auto" backend is
// left unresolved for them: detection would be wasted work, and a command that
// lists extensions or prints configuration must not ask which engine to use.
// Validation of the backend name only runs for the run-like commands.
var backendFreeActions = map[string]bool{
	"config":                  true,
	"review-target":           true,
	"tools":                   true,
	"features":                true,
	"extension-list":          true,
	cli.ActionExtensionManage: true,
}

func actionUsesBackend(action string) bool {
	return !backendFreeActions[action]
}

// backendPromptAllowed reports whether resolving the "auto" backend may ask the
// user. Structured output and --yes promise not to prompt, whatever the
// terminal looks like.
func backendPromptAllowed(parsed cli.Result) bool {
	if parsed.Options.PSJSON || parsed.Options.StatusJSON || parsed.ConfigView.JSON {
		return false
	}
	if parsed.ExtRequest != nil && (parsed.ExtRequest.JSON || parsed.ExtRequest.Yes) {
		return false
	}
	return true
}

// resolveBackend replaces the "auto" backend with the container engine this
// host has and points the shared CLI wrapper at it. It runs once, right after
// option resolution, because image builds and engine checks use the wrapper
// before any backend instance exists. An explicit docker that turns out to be
// the podman-docker shim is driven as podman. interactive permits the one-time
// engine question when both engines are installed.
func resolveBackend(opts *model.Options, interactive bool) {
	switch {
	case strings.TrimSpace(opts.Backend) == backend.NameAuto:
		opts.Backend = detectBackend(interactive)
	case opts.Backend == backend.NameDocker && dockerIsPodmanShim():
		// Detection maps the shim to podman; an explicit or configured docker
		// gets the same treatment, since driving the shim as Docker would skip
		// the user-namespace handling rootless podman needs.
		logx.Infof("docker on this host is the podman-docker shim; using podman")
		opts.Backend = backend.NamePodman
	}
	switch opts.Backend {
	case backend.NameDocker, backend.NamePodman:
		docker.SetBinary(opts.Backend)
	}
}

// detectBackend picks the engine for the "auto" backend. A single installed
// engine is used as is. With both installed, an interactive invocation on a
// terminal is asked once and the answer is saved to the global config;
// otherwise (scripts, captured output, --json or --yes) docker is used with a
// notice instead of blocking on a prompt. With no engine at all the docker
// default stays so the engine check reports what is missing.
func detectBackend(interactive bool) string {
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
	if !interactive || !backendPromptUsable() {
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
