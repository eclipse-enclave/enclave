// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
)

// newDevcontainerBackend builds a Docker backend whose only configured input is
// the devcontainer runArgs under test, plus the project dir used by the bind
// mount security check.
func newDevcontainerBackend(projectDir string, runArgs ...string) *Backend {
	return &Backend{opts: Options{
		ProjectDir:          projectDir,
		Host:                model.Host{Home: projectDir},
		DevcontainerRunArgs: runArgs,
	}}
}

// TestApplyDevcontainerRunArgsCharacterization exercises every runArg handler in
// a single pass against the live Docker-backend implementation and pins the full
// resulting ContainerConfig/HostConfig, including the value-append paths and the
// security blocking cases.
func TestApplyDevcontainerRunArgsCharacterization(t *testing.T) {
	projectDir := t.TempDir()
	envFile := filepath.Join(projectDir, "env")
	if err := os.WriteFile(envFile, []byte("C=3\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := newDevcontainerBackend(projectDir,
		"--env", "A=1",
		"-e", "B=2",
		"--env-file", envFile,
		"--volume", projectDir+":/data", // bind inside project -> allowed
		"--volume", "myvol:/v", // named volume -> passthrough
		"--mount", "type=volume,source=mvol,target=/m",
		"--tmpfs", "/tmp:size=64m",
		"--security-opt", "label=type:container_t", // safe -> kept
		"--security-opt", "seccomp=unconfined", // dangerous -> dropped
		"--cap-add", "NET_BIND_SERVICE", // safe -> kept
		"--cap-add", "SYS_ADMIN", // dangerous -> dropped
		"--cap-drop", "MKNOD",
		"--add-host", "host.docker.internal:host-gateway",
		"--hostname", "myhost",
		"--workdir", "/work",
		"--init",
		"--network", "host", // managed by enclave -> blocked
		"--net=bridge",            // short =form -> blocked
		"--entrypoint", "/bin/sh", // blocked, consumes its value
		"--privileged",   // blocked
		"--user", "1000", // blocked, skips the following value
		"--bogus-flag", // unknown -> warned, ignored
	)

	config := &dockercmd.ContainerConfig{}
	hostConfig := &dockercmd.HostConfig{}
	out := captureStderr(t, func() {
		b.applyDevcontainerRunArgs(config, hostConfig)
	})

	if got := config.Env; !reflect.DeepEqual(got, []string{"A=1", "B=2", "C=3"}) {
		t.Errorf("Env = %v, want [A=1 B=2 C=3]", got)
	}
	if config.Hostname != "myhost" {
		t.Errorf("Hostname = %q, want %q", config.Hostname, "myhost")
	}
	if config.WorkingDir != "/work" {
		t.Errorf("WorkingDir = %q, want %q", config.WorkingDir, "/work")
	}
	if hostConfig.Init == nil || !*hostConfig.Init {
		t.Errorf("Init = %v, want true", hostConfig.Init)
	}
	if got := hostConfig.Tmpfs; !reflect.DeepEqual(got, map[string]string{"/tmp": "size=64m"}) {
		t.Errorf("Tmpfs = %v, want map[/tmp:size=64m]", got)
	}
	if got := hostConfig.SecurityOpt; !reflect.DeepEqual(got, []string{"label=type:container_t"}) {
		t.Errorf("SecurityOpt = %v, want [label=type:container_t] (seccomp=unconfined dropped)", got)
	}
	if got := hostConfig.CapAdd; !reflect.DeepEqual(got, []string{"NET_BIND_SERVICE"}) {
		t.Errorf("CapAdd = %v, want [NET_BIND_SERVICE] (SYS_ADMIN dropped)", got)
	}
	if got := hostConfig.CapDrop; !reflect.DeepEqual(got, []string{"MKNOD"}) {
		t.Errorf("CapDrop = %v, want [MKNOD]", got)
	}
	if got := hostConfig.ExtraHosts; !reflect.DeepEqual(got, []string{"host.docker.internal:host-gateway"}) {
		t.Errorf("ExtraHosts = %v, want [host.docker.internal:host-gateway]", got)
	}

	if len(hostConfig.Mounts) != 3 {
		t.Fatalf("want 3 mounts, got %d: %+v", len(hostConfig.Mounts), hostConfig.Mounts)
	}
	assertMountPresent(t, hostConfig.Mounts, dockercmd.MountTypeBind, projectDir, "/data")
	assertMountPresent(t, hostConfig.Mounts, dockercmd.MountTypeVolume, "myvol", "/v")
	assertMountPresent(t, hostConfig.Mounts, dockercmd.MountTypeVolume, "mvol", "/m")

	if hostConfig.Privileged {
		t.Errorf("Privileged must remain false after a blocked --privileged runArg")
	}
	for _, e := range config.Env {
		if e == "1000" {
			t.Errorf("--user value 1000 leaked into Env %v", config.Env)
		}
	}

	for _, want := range []string{"blocked", "not supported"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected a warning containing %q; got:\n%s", want, out)
		}
	}
}

func TestApplyDevcontainerRunArgsBlocksPrivileged(t *testing.T) {
	cases := []struct {
		name    string
		runArgs []string
	}{
		{name: "bare flag", runArgs: []string{"--privileged"}},
		{name: "equals form true", runArgs: []string{"--privileged=true"}},
		{name: "equals form false", runArgs: []string{"--privileged=false"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newDevcontainerBackend(t.TempDir(), tc.runArgs...)
			config := &dockercmd.ContainerConfig{}
			hostConfig := &dockercmd.HostConfig{Privileged: true}
			out := captureStderr(t, func() {
				b.applyDevcontainerRunArgs(config, hostConfig)
			})

			if !hostConfig.Privileged {
				t.Fatalf("expected hostConfig.Privileged to remain unchanged (true) when devcontainer runArgs requests %v; got false", tc.runArgs)
			}
			if !strings.Contains(out, "blocked for security") {
				t.Fatalf("expected security-aware warning for runArgs %v; got: %q", tc.runArgs, out)
			}
			if strings.Contains(out, "is not supported") {
				t.Fatalf("expected runArgs %v to be recognized as --privileged; got generic 'not supported' warning: %q", tc.runArgs, out)
			}
		})
	}
}

func TestApplyDevcontainerRunArgsBlocksDangerousCapAdd(t *testing.T) {
	cases := []struct {
		name       string
		runArgs    []string
		wantCapAdd []string
	}{
		{name: "drops SYS_ADMIN", runArgs: []string{"--cap-add", "SYS_ADMIN"}, wantCapAdd: nil},
		{name: "drops SYS_PTRACE", runArgs: []string{"--cap-add", "SYS_PTRACE"}, wantCapAdd: nil},
		{name: "drops NET_ADMIN", runArgs: []string{"--cap-add", "NET_ADMIN"}, wantCapAdd: nil},
		{name: "drops DAC_OVERRIDE", runArgs: []string{"--cap-add", "DAC_OVERRIDE"}, wantCapAdd: nil},
		{name: "drops SYS_RAWIO", runArgs: []string{"--cap-add", "SYS_RAWIO"}, wantCapAdd: nil},
		{name: "drops SYS_MODULE", runArgs: []string{"--cap-add", "SYS_MODULE"}, wantCapAdd: nil},
		{name: "drops ALL", runArgs: []string{"--cap-add", "ALL"}, wantCapAdd: nil},
		{name: "drops CAP_SYS_ADMIN with prefix", runArgs: []string{"--cap-add", "CAP_SYS_ADMIN"}, wantCapAdd: nil},
		{name: "drops cap_sys_admin lowercase prefix", runArgs: []string{"--cap-add", "cap_sys_admin"}, wantCapAdd: nil},
		{name: "drops Cap_Sys_Admin mixed-case prefix", runArgs: []string{"--cap-add", "Cap_Sys_Admin"}, wantCapAdd: nil},
		{name: "drops leading-whitespace SYS_ADMIN", runArgs: []string{"--cap-add", " SYS_ADMIN"}, wantCapAdd: nil},
		{name: "drops trailing-whitespace SYS_ADMIN", runArgs: []string{"--cap-add", "SYS_ADMIN "}, wantCapAdd: nil},
		{name: "drops ALL case-insensitive", runArgs: []string{"--cap-add", "all"}, wantCapAdd: nil},
		{name: "passes NET_BIND_SERVICE through", runArgs: []string{"--cap-add", "NET_BIND_SERVICE"}, wantCapAdd: []string{"NET_BIND_SERVICE"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newDevcontainerBackend(t.TempDir(), tc.runArgs...)
			config := &dockercmd.ContainerConfig{}
			hostConfig := &dockercmd.HostConfig{}
			b.applyDevcontainerRunArgs(config, hostConfig)

			if !reflect.DeepEqual(hostConfig.CapAdd, tc.wantCapAdd) {
				t.Fatalf("CapAdd: got %v, want %v", hostConfig.CapAdd, tc.wantCapAdd)
			}
		})
	}
}

func TestApplyDevcontainerRunArgsBlocksDangerousSecurityOpts(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "seccomp_unconfined", value: "seccomp=unconfined"},
		{name: "apparmor_unconfined", value: "apparmor=unconfined"},
		{name: "no_new_privileges_false", value: "no-new-privileges=false"},
		{name: "no_new_privileges_zero", value: "no-new-privileges=0"},
		{name: "label_disable", value: "label=disable"},
		{name: "mixed_case_seccomp", value: "Seccomp=Unconfined"},
		{name: "padded_apparmor", value: "  apparmor=unconfined  "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newDevcontainerBackend(t.TempDir(), "--security-opt", tc.value)
			config := &dockercmd.ContainerConfig{}
			hostConfig := &dockercmd.HostConfig{}
			b.applyDevcontainerRunArgs(config, hostConfig)

			if len(hostConfig.SecurityOpt) != 0 {
				t.Fatalf("expected no security opts to be applied, got %v", hostConfig.SecurityOpt)
			}
		})
	}
}

func TestApplyDevcontainerRunArgsAllowsSafeSecurityOpt(t *testing.T) {
	b := newDevcontainerBackend(t.TempDir(), "--security-opt", "label=type:container_t")
	config := &dockercmd.ContainerConfig{}
	hostConfig := &dockercmd.HostConfig{}
	b.applyDevcontainerRunArgs(config, hostConfig)

	if len(hostConfig.SecurityOpt) != 1 || hostConfig.SecurityOpt[0] != "label=type:container_t" {
		t.Fatalf("expected safe --security-opt to pass through, got %v", hostConfig.SecurityOpt)
	}
}

func TestApplyDevcontainerRunArgsSecurityOptEqualsForm(t *testing.T) {
	b := newDevcontainerBackend(t.TempDir(), "--security-opt=seccomp=unconfined")
	config := &dockercmd.ContainerConfig{}
	hostConfig := &dockercmd.HostConfig{}
	b.applyDevcontainerRunArgs(config, hostConfig)

	if len(hostConfig.SecurityOpt) != 0 {
		t.Fatalf("expected --security-opt=seccomp=unconfined to be blocked, got %v", hostConfig.SecurityOpt)
	}
}

func TestApplyDevcontainerRunArgsSecurityOptPreservesExistingEntries(t *testing.T) {
	b := newDevcontainerBackend(t.TempDir(), "--security-opt", "seccomp=unconfined")
	config := &dockercmd.ContainerConfig{}
	hostConfig := &dockercmd.HostConfig{SecurityOpt: []string{"label=type:container_t"}}
	b.applyDevcontainerRunArgs(config, hostConfig)

	want := []string{"label=type:container_t"}
	if !reflect.DeepEqual(hostConfig.SecurityOpt, want) {
		t.Fatalf("SecurityOpt: got %v, want %v", hostConfig.SecurityOpt, want)
	}
}

func TestApplyDevcontainerRunArgsNetworkModeIsIgnored(t *testing.T) {
	const sentinel dockercmd.NetworkMode = "enclave-gateway"
	cases := []struct {
		name    string
		runArgs []string
	}{
		{name: "network host", runArgs: []string{"--network", "host"}},
		{name: "network bridge", runArgs: []string{"--network", "bridge"}},
		{name: "network equals form", runArgs: []string{"--network=host"}},
		{name: "net alias separate", runArgs: []string{"--net", "host"}},
		{name: "net alias equals form", runArgs: []string{"--net=host"}},
		{name: "network none", runArgs: []string{"--network", "none"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newDevcontainerBackend(t.TempDir(), tc.runArgs...)
			config := &dockercmd.ContainerConfig{}
			hostConfig := &dockercmd.HostConfig{NetworkMode: sentinel}

			b.applyDevcontainerRunArgs(config, hostConfig)

			if hostConfig.NetworkMode != sentinel {
				t.Fatalf("expected NetworkMode to remain %q for runArgs %v, got %q", sentinel, tc.runArgs, hostConfig.NetworkMode)
			}
		})
	}
}

func TestApplyDevcontainerRunArgsVolumeSystemDirIsDropped(t *testing.T) {
	b := newDevcontainerBackend(t.TempDir(), "--volume", "/etc:/etc")
	config := &dockercmd.ContainerConfig{}
	hostConfig := &dockercmd.HostConfig{}
	b.applyDevcontainerRunArgs(config, hostConfig)

	for _, m := range hostConfig.Mounts {
		if m.Type == dockercmd.MountTypeBind && m.Source == "/etc" {
			t.Fatalf("expected --volume /etc:/etc to be dropped, got %+v", hostConfig.Mounts)
		}
	}
}

func TestApplyDevcontainerRunArgsVolumeProjectPathIsAllowed(t *testing.T) {
	projectDir := t.TempDir()
	b := newDevcontainerBackend(projectDir, "--volume", projectDir+":/data")
	config := &dockercmd.ContainerConfig{}
	hostConfig := &dockercmd.HostConfig{}
	b.applyDevcontainerRunArgs(config, hostConfig)

	found := false
	for _, m := range hostConfig.Mounts {
		if m.Type == dockercmd.MountTypeBind && m.Source == projectDir && m.Target == "/data" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected project bind mount to be present, got %+v", hostConfig.Mounts)
	}
}

func TestApplyDevcontainerRunArgsProjectMountReadonlyForcesBindMountReadonly(t *testing.T) {
	projectDir := t.TempDir()
	b := newDevcontainerBackend(projectDir, "--volume", projectDir+":/data")
	b.opts.ProjectMount = model.ProjectMountReadonly
	config := &dockercmd.ContainerConfig{}
	hostConfig := &dockercmd.HostConfig{}
	b.applyDevcontainerRunArgs(config, hostConfig)

	if len(hostConfig.Mounts) != 1 {
		t.Fatalf("expected one mount, got %+v", hostConfig.Mounts)
	}
	if !hostConfig.Mounts[0].ReadOnly {
		t.Fatalf("expected devcontainer runArg bind mount to be read-only: %+v", hostConfig.Mounts[0])
	}
}

func TestApplyDevcontainerRunArgsMountSystemDirIsDropped(t *testing.T) {
	b := newDevcontainerBackend(t.TempDir(), "--mount", "type=bind,source=/root/.ssh,target=/ssh")
	config := &dockercmd.ContainerConfig{}
	hostConfig := &dockercmd.HostConfig{}
	b.applyDevcontainerRunArgs(config, hostConfig)

	for _, m := range hostConfig.Mounts {
		if m.Type == dockercmd.MountTypeBind && m.Source == "/root/.ssh" {
			t.Fatalf("expected --mount /root/.ssh to be dropped, got %+v", hostConfig.Mounts)
		}
	}
}

func TestApplyDevcontainerRunArgsNamedVolumePassesThrough(t *testing.T) {
	b := newDevcontainerBackend(t.TempDir(),
		"--volume", "myvolume:/data",
		"--mount", "type=volume,source=othervol,target=/cache",
	)
	config := &dockercmd.ContainerConfig{}
	hostConfig := &dockercmd.HostConfig{}
	b.applyDevcontainerRunArgs(config, hostConfig)

	foundVolume := false
	foundNamed := false
	for _, m := range hostConfig.Mounts {
		if m.Type == dockercmd.MountTypeVolume && m.Source == "myvolume" && m.Target == "/data" {
			foundVolume = true
		}
		if m.Type == dockercmd.MountTypeVolume && m.Source == "othervol" && m.Target == "/cache" {
			foundNamed = true
		}
	}
	if !foundVolume {
		t.Fatalf("expected --volume myvolume:/data to pass through, got %+v", hostConfig.Mounts)
	}
	if !foundNamed {
		t.Fatalf("expected --mount type=volume named volume to pass through, got %+v", hostConfig.Mounts)
	}
}

func TestApplyDevcontainerRunArgsVolumeSymlinkToSystemDirIsDropped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	projectDir := t.TempDir()
	symlinkPath := filepath.Join(projectDir, "creds")
	if err := os.Symlink("/etc", symlinkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	b := newDevcontainerBackend(projectDir, "--volume", symlinkPath+":/x")
	config := &dockercmd.ContainerConfig{}
	hostConfig := &dockercmd.HostConfig{}
	b.applyDevcontainerRunArgs(config, hostConfig)

	for _, m := range hostConfig.Mounts {
		if m.Type == dockercmd.MountTypeBind {
			t.Fatalf("expected --volume symlink-to-/etc to be dropped, got %+v", hostConfig.Mounts)
		}
	}
}

func TestApplyDevcontainerRunArgsBlocksEntrypointOverride(t *testing.T) {
	cases := []struct {
		name    string
		runArgs []string
	}{
		{name: "separate value", runArgs: []string{"--entrypoint", "/bin/bash"}},
		{name: "equals form", runArgs: []string{"--entrypoint=/bin/sh"}},
		{name: "bare flag", runArgs: []string{"--entrypoint"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newDevcontainerBackend(t.TempDir(), tc.runArgs...)
			original := []string{"/usr/local/bin/entrypoint.sh"}
			config := &dockercmd.ContainerConfig{Entrypoint: append([]string(nil), original...)}
			hostConfig := &dockercmd.HostConfig{}

			b.applyDevcontainerRunArgs(config, hostConfig)

			if len(config.Entrypoint) != len(original) || config.Entrypoint[0] != original[0] {
				t.Fatalf("expected entrypoint to be preserved as %v for runArgs %v, got %v", original, tc.runArgs, config.Entrypoint)
			}
		})
	}
}

func assertMountPresent(t *testing.T, mounts []dockercmd.Mount, typ dockercmd.MountType, source, target string) {
	t.Helper()
	for _, m := range mounts {
		if m.Type == typ && m.Source == source && m.Target == target {
			return
		}
	}
	t.Errorf("expected mount {%s %s %s} present in %+v", typ, source, target, mounts)
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stderr = writer

	defer func() {
		os.Stderr = original
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(data)
}
