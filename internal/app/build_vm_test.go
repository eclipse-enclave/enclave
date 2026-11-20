// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"enclave/internal/backend"
	backendvm "enclave/internal/backend/vm"
	"enclave/internal/config"
	"enclave/internal/docker"
	"enclave/internal/model"
)

func TestSelectBackendVM(t *testing.T) {
	if err := validateBackendName(backend.NameVM); err != nil {
		t.Fatalf("vm is not accepted as a backend name: %v", err)
	}
	be, err := selectBackend(model.Options{RunOptions: model.RunOptions{Backend: backend.NameVM}}, dockerBackendOptions(model.Host{}, model.Paths{}, model.BuildOptions{}, model.RunOptions{}))
	if err != nil || be.Name() != backend.NameVM {
		t.Fatalf("selectBackend = %v, %v", be, err)
	}
	if err := validateBackendName("vmx"); err == nil || !strings.Contains(err.Error(), "qemu, vm") {
		t.Fatalf("unknown backend error = %v, want the vm backend listed", err)
	}
}

func TestVMBackendNeverRequiresDocker(t *testing.T) {
	for _, action := range []string{"run", "shell", "exec"} {
		if executionRequiresDocker(action, model.Options{RunOptions: model.RunOptions{Backend: backend.NameVM}}) {
			t.Errorf("%s with the vm backend requires Docker, which nothing in it uses", action)
		}
	}
}

// ps, attach and stop find vm sessions in files and VMs. They work on a host
// without a container engine, where the other backends' check fails.
func TestVMSessionCommandsNeedNoContainerEngine(t *testing.T) {
	previousPing := dockerPing
	dockerPing = func(context.Context) error { return errors.New("no engine on this host") }
	t.Cleanup(func() { dockerPing = previousPing })
	vm := model.Options{RunOptions: model.RunOptions{Backend: backend.NameVM}}
	if code := requireSessionEngine(vm); code != 0 {
		t.Errorf("requireSessionEngine(vm) = %d", code)
	}
	for _, name := range []string{backend.NameDocker, backend.NameQEMU} {
		if code := requireSessionEngine(model.Options{RunOptions: model.RunOptions{Backend: name}}); code == 0 {
			t.Errorf("requireSessionEngine(%s) passed without an engine", name)
		}
	}

	psSessionList = func(context.Context, model.Options) ([]backend.Session, error) {
		return []backend.Session{{Ref: backend.SessionRef{Name: "enclave-codex-abc123abc123-1"}, Tool: "codex", Status: "running", Background: true, Name: "1"}}, nil
	}
	t.Cleanup(func() { psSessionList = listPSSessions })
	vm.PSJSON = true
	out := captureStdout(t, func() {
		if code := runPS(vm); code != 0 {
			t.Errorf("ps --backend vm = %d without a container engine", code)
		}
	})
	for _, want := range []string{`"name": "enclave-codex-abc123abc123-1"`, `"status": "running"`, `"background": true`, `"network": null`} {
		if !strings.Contains(out, want) {
			t.Errorf("ps --json lacks %s:\n%s", want, out)
		}
	}
}

func TestValidateOptionsCoercesVMRuns(t *testing.T) {
	paths := testPathsWithFeatureDefaults(t)
	opts := model.Options{
		RunOptions:   model.RunOptions{Backend: backend.NameVM, Tool: "codex"},
		BuildOptions: model.BuildOptions{ImageName: "test"},
	}
	got, _, warnings, err := ValidateOptions(opts, model.DefaultOptionSources(), ValidationContext{Action: "run", Paths: paths})
	if err != nil {
		t.Fatalf("ValidateOptions: %v", err)
	}
	if !got.AllowAllNetwork || !got.DisableGateway || !got.Slim {
		t.Errorf("vm run not coerced: allow-all=%v disable-gateway=%v slim=%v", got.AllowAllNetwork, got.DisableGateway, got.Slim)
	}
	if !slices.ContainsFunc(warnings, func(w string) bool {
		return strings.HasPrefix(w, "vm backend: network isolation is unavailable")
	}) {
		t.Errorf("no notice about the missing network isolation in %v", warnings)
	}
	if !slices.ContainsFunc(warnings, func(w string) bool { return strings.HasPrefix(w, "vm backend: tool-only base image") }) {
		t.Errorf("no tool-only notice in %v", warnings)
	}

	// A request for restricted egress must fail instead of running unrestricted.
	restricted := opts
	restricted.AllowDomains = []string{"example.com"}
	sources := model.DefaultOptionSources()
	sources.AllowDomains = model.SourceProject
	if _, _, _, err := ValidateOptions(restricted, sources, ValidationContext{Action: "run", Paths: paths}); err == nil || !strings.HasPrefix(err.Error(), "vm backend has no network gateway") {
		t.Errorf("allow_domains with the vm backend = %v", err)
	}
	// Detached sessions are a qemu limit, not a vm one.
	detached := opts
	detached.Background = true
	if _, _, _, err := ValidateOptions(detached, model.DefaultOptionSources(), ValidationContext{Action: "run", Paths: paths}); err != nil {
		t.Errorf("--background with the vm backend = %v", err)
	}
	detached.Backend = backend.NameQEMU
	if _, _, _, err := ValidateOptions(detached, model.DefaultOptionSources(), ValidationContext{Action: "run", Paths: paths}); err == nil || !strings.Contains(err.Error(), "qemu backend runs foreground sessions only") {
		t.Errorf("--background with the qemu backend = %v", err)
	}
	devcontainer := opts
	devcontainer.Devcontainer = true
	if _, _, _, err := ValidateOptions(devcontainer, model.DefaultOptionSources(), ValidationContext{Action: "run", Paths: paths}); err == nil || !strings.Contains(err.Error(), "devcontainer mode requires") {
		t.Errorf("devcontainer with the vm backend = %v", err)
	}
}

func TestEnsureVMBaseAcceptsOnlyACompletePrebuiltBase(t *testing.T) {
	opts := model.Options{}
	opts.ImageNameSet = true
	baseDir := t.TempDir()
	if _, code := ensureVMBase(&CommandInput{}, opts, buildConfig{ImageName: baseDir}, model.Host{Home: t.TempDir()}, model.Project{}); code == 0 {
		t.Fatal("an empty directory was accepted as a base image")
	}
	writeVMBase(t, baseDir, "k")
	got, code := ensureVMBase(&CommandInput{}, opts, buildConfig{ImageName: baseDir}, model.Host{Home: t.TempDir()}, model.Project{})
	if code != 0 || got.ImageName != baseDir {
		t.Fatalf("ensureVMBase = %+v, %d", got, code)
	}
}

func writeVMBase(t *testing.T, dir string, key string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"base.qcow2": "disk", backendvm.BaseStampFile: `{"key":"` + key + `","tool":"codex"}`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// vmBaseFixture returns what a managed base lookup needs, with the container
// CLI replaced by a stub that records being run.
func vmBaseFixture(t *testing.T) (input *CommandInput, opts model.Options, host model.Host, engineRan func() bool) {
	t.Helper()
	paths := writeTestCodexToolPaths(t, false)
	host = model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}
	marker := filepath.Join(t.TempDir(), "engine-ran")
	stub := filepath.Join(t.TempDir(), "engine")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := docker.Binary()
	docker.SetBinary(stub)
	t.Cleanup(func() { docker.SetBinary(previous) })
	opts = model.Options{
		RunOptions:   model.RunOptions{Backend: backend.NameVM, Tool: "codex"},
		BuildOptions: model.BuildOptions{Slim: true, NoRebuild: true, Features: []string{}},
	}
	return &CommandInput{Ctx: NewAppContext(paths, "")}, opts, host, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}
}

// currentVMBaseDir is where ensureVMBase looks for the base of the fixture.
func currentVMBaseDir(t *testing.T, input *CommandInput, opts model.Options, host model.Host) string {
	t.Helper()
	selection, err := resolveRuntimeImageSelection(input.Ctx.Paths, opts.BuildOptions, opts.Tool)
	if err != nil {
		t.Fatal(err)
	}
	combined, err := runtimeImageCombinedHash(input.Ctx.Paths, buildConfig{ImageName: "enclave-codex:test"}, selection)
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := runtimeAssetDirHash(input.Ctx.Paths.AppRoot, "vm/base")
	if err != nil {
		t.Fatal(err)
	}
	return backendvm.BaseDir(host.Home, "codex", backendvm.BaseKey(combined, recipe, host))
}

func TestEnsureVMBaseLooksUpTheManagedBaseWithoutAnEngine(t *testing.T) {
	input, opts, host, engineRan := vmBaseFixture(t)
	buildCfg := buildConfig{ImageName: "enclave-codex:test"}
	// --no-rebuild without a base: an error, not a build.
	if _, code := ensureVMBase(input, opts, buildCfg, host, model.Project{Hash: "abc123def456"}); code == 0 {
		t.Fatal("a missing base was accepted under --no-rebuild")
	}
	baseDir := currentVMBaseDir(t, input, opts, host)
	writeVMBase(t, baseDir, "current")
	got, code := ensureVMBase(input, opts, buildCfg, host, model.Project{Hash: "abc123def456"})
	if code != 0 || got.ImageName != baseDir {
		t.Fatalf("ensureVMBase = %+v, %d; want the managed base %s", got, code, baseDir)
	}
	if engineRan() {
		t.Fatal("the base lookup ran the container engine; the vm backend is documented to work without one")
	}
}

// A project with a disk restarts on that disk's base, also offline and after
// the inputs of new bases changed.
func TestEnsureVMBaseKeepsTheBaseOfAnExistingDisk(t *testing.T) {
	input, opts, host, engineRan := vmBaseFixture(t)
	project := model.Project{Hash: "abc123def456"}
	earlier := backendvm.BaseDir(host.Home, "codex", "earlierkey")
	writeVMBase(t, earlier, "earlierkey")
	diskDir := config.HostProjectVMDir(host.Home, project.Hash, "codex")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"baseKey":"earlierkey","base":"` + filepath.Join(earlier, "base.qcow2") + `"}`
	for name, content := range map[string]string{"disk.qcow2": "overlay", "disk.json": meta} {
		if err := os.WriteFile(filepath.Join(diskDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The base a new disk would get does not exist, and must not be needed.
	got, code := ensureVMBase(input, opts, buildConfig{ImageName: "enclave-codex:test"}, host, project)
	if code != 0 || got.ImageName != earlier {
		t.Fatalf("ensureVMBase = %+v, %d; want the disk's base %s", got, code, earlier)
	}
	// Another project has no disk and still needs the current base.
	if _, code := ensureVMBase(input, opts, buildConfig{ImageName: "enclave-codex:test"}, host, model.Project{Hash: "fff000fff000"}); code == 0 {
		t.Fatal("a project without a disk was given another project's base")
	}
	if engineRan() {
		t.Fatal("the base lookup ran the container engine")
	}
}
