// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"strings"
	"testing"

	"enclave/internal/backend"
	backenddocker "enclave/internal/backend/docker"
	"enclave/internal/cli"
	"enclave/internal/docker"
	"enclave/internal/extinstall"
	"enclave/internal/model"
)

// stubBackendResolution replaces the resolution seams for one test: the
// detected CLIs, whether a prompt could be shown, and where a choice is saved.
func stubBackendResolution(t *testing.T, clis []string, interactive bool) *[]string {
	t.Helper()
	previousBinary := docker.Binary()
	previousDetect, previousShim, previousPrompt, previousSave := detectContainerCLIs, dockerIsPodmanShim, backendPromptUsable, saveBackendChoice
	t.Cleanup(func() {
		docker.SetBinary(previousBinary)
		detectContainerCLIs, dockerIsPodmanShim, backendPromptUsable, saveBackendChoice = previousDetect, previousShim, previousPrompt, previousSave
	})
	saved := &[]string{}
	detectContainerCLIs = func() []string { return clis }
	dockerIsPodmanShim = func() bool { return false }
	backendPromptUsable = func() bool { return interactive }
	saveBackendChoice = func(key string, value string) (string, error) {
		*saved = append(*saved, key+"="+value)
		return "config.json", nil
	}
	return saved
}

func TestResolveBackendUsesTheOnlyInstalledEngine(t *testing.T) {
	for _, tc := range []struct {
		clis []string
		want string
	}{
		{clis: []string{backend.NamePodman}, want: backend.NamePodman},
		{clis: []string{backend.NameDocker}, want: backend.NameDocker},
		// No engine: keep docker so the engine check reports what is missing.
		{clis: nil, want: backend.NameDocker},
	} {
		saved := stubBackendResolution(t, tc.clis, true)
		opts := model.Options{RunOptions: model.RunOptions{Backend: backend.NameAuto}}
		resolveBackend(&opts, true)
		if opts.Backend != tc.want {
			t.Fatalf("clis %v: Backend = %q, want %q", tc.clis, opts.Backend, tc.want)
		}
		if len(*saved) != 0 {
			t.Fatalf("clis %v: nothing should be saved without a prompt, got %v", tc.clis, *saved)
		}
		if got := docker.Binary(); got != tc.want {
			t.Fatalf("clis %v: container CLI = %q, want %q", tc.clis, got, tc.want)
		}
	}
}

func TestResolveBackendWithBothEnginesNonInteractiveUsesDocker(t *testing.T) {
	saved := stubBackendResolution(t, []string{backend.NameDocker, backend.NamePodman}, false)
	opts := model.Options{RunOptions: model.RunOptions{Backend: backend.NameAuto}}
	resolveBackend(&opts, true)
	if opts.Backend != backend.NameDocker {
		t.Fatalf("Backend = %q, want docker without a terminal", opts.Backend)
	}
	if len(*saved) != 0 {
		t.Fatalf("non-interactive resolution must not write config, got %v", *saved)
	}
}

func TestResolveBackendLeavesExplicitChoiceAlone(t *testing.T) {
	stubBackendResolution(t, []string{backend.NameDocker, backend.NamePodman}, true)
	for _, name := range []string{backend.NameDocker, backend.NamePodman, backend.NameQEMU} {
		opts := model.Options{RunOptions: model.RunOptions{Backend: name}}
		resolveBackend(&opts, true)
		if opts.Backend != name {
			t.Fatalf("explicit backend %q was changed to %q", name, opts.Backend)
		}
	}
	if docker.Binary() != backend.NamePodman {
		t.Fatalf("explicit podman must switch the container CLI, got %q", docker.Binary())
	}
}

func TestSelectBackendPodmanUsesDockerBackend(t *testing.T) {
	stubBackendResolution(t, []string{backend.NamePodman}, false)
	opts := model.Options{RunOptions: model.RunOptions{Backend: backend.NamePodman}}
	resolveBackend(&opts, true)
	be, err := selectBackend(opts, backenddocker.Options{})
	if err != nil {
		t.Fatalf("selectBackend: %v", err)
	}
	if be.Name() != backend.NamePodman {
		t.Fatalf("backend name = %q, want %q", be.Name(), backend.NamePodman)
	}
	if _, err := selectBackend(model.Options{RunOptions: model.RunOptions{Backend: "lxc"}}, backenddocker.Options{}); err == nil || !strings.Contains(err.Error(), "podman") {
		t.Fatalf("expected unsupported-backend error listing podman, got %v", err)
	}
}

func TestValidateOptionsAcceptsPodmanBackend(t *testing.T) {
	opts := model.Options{
		RunOptions:   model.RunOptions{Backend: backend.NamePodman, Tool: "claude"},
		BuildOptions: model.BuildOptions{ImageName: "test"},
	}
	got, _, _, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Action: "run"})
	if err != nil {
		t.Fatalf("podman backend should validate, got %v", err)
	}
	if got.Backend != backend.NamePodman {
		t.Fatalf("Backend = %q, want podman", got.Backend)
	}
	if got.AllowAllNetwork || got.Slim {
		t.Fatal("podman must keep the full docker feature set (no qemu coercion)")
	}

	opts.Devcontainer = true
	if _, _, _, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Action: "run"}); err == nil {
		t.Fatal("devcontainer mode is only verified with docker and must be rejected for podman")
	}
}

// A --json or --yes invocation must never block on the engine question, even
// when stdin and stderr are terminals.
func TestResolveBackendNonInteractiveInvocationSkipsPrompt(t *testing.T) {
	saved := stubBackendResolution(t, []string{backend.NameDocker, backend.NamePodman}, true)
	opts := model.Options{RunOptions: model.RunOptions{Backend: backend.NameAuto}}
	resolveBackend(&opts, false)
	if opts.Backend != backend.NameDocker {
		t.Fatalf("Backend = %q, want docker for a non-interactive invocation", opts.Backend)
	}
	if len(*saved) != 0 {
		t.Fatalf("non-interactive resolution must not write config, got %v", *saved)
	}
}

func TestBackendPromptAllowed(t *testing.T) {
	cases := []struct {
		name   string
		parsed cli.Result
		want   bool
	}{
		{name: "plain run", parsed: cli.Result{Action: "run"}, want: true},
		{name: "ps --json", parsed: cli.Result{Action: "ps", Options: model.Options{PSOptions: model.PSOptions{PSJSON: true}}}, want: false},
		{name: "status --json", parsed: cli.Result{Action: "status", Options: model.Options{StatusOptions: model.StatusOptions{StatusJSON: true}}}, want: false},
		{name: "config view --json", parsed: cli.Result{Action: "config", ConfigView: model.ConfigView{JSON: true}}, want: false},
		{name: "tools add --json --yes", parsed: cli.Result{Action: cli.ActionExtensionManage, ExtRequest: &extinstall.Request{JSON: true, Yes: true}}, want: false},
		{name: "tools add --yes", parsed: cli.Result{Action: cli.ActionExtensionManage, ExtRequest: &extinstall.Request{Yes: true}}, want: false},
		{name: "tools add interactive", parsed: cli.Result{Action: cli.ActionExtensionManage, ExtRequest: &extinstall.Request{Interactive: true}}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := backendPromptAllowed(tc.parsed); got != tc.want {
				t.Fatalf("backendPromptAllowed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestActionUsesBackend(t *testing.T) {
	for _, action := range []string{"tools", "features", "extension-list", cli.ActionExtensionManage, "config", "review-target"} {
		if actionUsesBackend(action) {
			t.Fatalf("%s never touches an engine and must not resolve the backend", action)
		}
	}
	for _, action := range []string{"run", "shell", "exec", "update", "info", "ps", "status", "stop", "attach", "cleanup", "theia", "img-import", "network-apply", "devcontainer-generate"} {
		if !actionUsesBackend(action) {
			t.Fatalf("%s uses an engine and must resolve the backend", action)
		}
	}
}

// A host with the podman-docker shim has users who type --backend docker or
// wrote backend: docker before podman was a backend; the shim is podman and
// must be driven as such.
func TestResolveBackendExplicitDockerOnShimHostUsesPodman(t *testing.T) {
	saved := stubBackendResolution(t, []string{backend.NamePodman}, true)
	dockerIsPodmanShim = func() bool { return true }
	opts := model.Options{RunOptions: model.RunOptions{Backend: backend.NameDocker}}
	resolveBackend(&opts, true)
	if opts.Backend != backend.NamePodman {
		t.Fatalf("Backend = %q, want podman behind the docker shim", opts.Backend)
	}
	if got := docker.Binary(); got != backend.NamePodman {
		t.Fatalf("container CLI = %q, want podman", got)
	}
	if len(*saved) != 0 {
		t.Fatalf("the shim switch must not write config, got %v", *saved)
	}
}
