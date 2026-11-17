// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"slices"
	"strings"
	"testing"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/util"
)

func usePodmanCLI(t *testing.T) {
	t.Helper()
	previous := dockercmd.Binary()
	dockercmd.SetBinary(backend.NamePodman)
	t.Cleanup(func() { dockercmd.SetBinary(previous) })
}

func TestPodmanSessionsKeepHostUserNamespace(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := New(Options{Host: model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}})
	req := backend.Request{
		Session: backend.SessionMeta{Tool: "codex", ProjectHash: "abc123abc123", Name: "enclave-codex-abc123abc123"},
		Image:   "enclave-test:latest",
	}

	if spec := b.dockerConfig(req); spec.hostConfig.UserNS != "" {
		t.Fatalf("docker sessions must not set --userns, got %q", spec.hostConfig.UserNS)
	}
	if got := b.Name(); got != backend.NameDocker {
		t.Fatalf("Name() = %q, want %q", got, backend.NameDocker)
	}
	if got := New(Options{Engine: backend.NamePodman}).Name(); got != backend.NamePodman {
		t.Fatalf("Name() = %q, want %q for the podman engine", got, backend.NamePodman)
	}

	usePodmanCLI(t)
	if spec := b.dockerConfig(req); spec.hostConfig.UserNS != "keep-id" {
		t.Fatalf("podman sessions must run with --userns keep-id, got %q", spec.hostConfig.UserNS)
	}

	helper := sharedAuthSyncHostConfig("/config", "/auth", false)
	applyHostUserNamespace(helper)
	if helper.UserNS != "keep-id" {
		t.Fatalf("auth reconcile helper must run with --userns keep-id under podman, got %q", helper.UserNS)
	}
	if got := b.authSyncHelperUser(); got != "1000:1000" {
		t.Fatalf("auth reconcile helper must run as the host user under podman, got %q", got)
	}
}

func TestAuthSyncHelperRunsAsRootUnderDocker(t *testing.T) {
	b := New(Options{Host: model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}})
	if got := b.authSyncHelperUser(); got != "root" {
		t.Fatalf("auth reconcile helper user = %q, want root", got)
	}
}

func TestPodmanSessionsJoinGatewayUserNamespace(t *testing.T) {
	hostConfig := &dockercmd.HostConfig{}
	joinGatewayNamespaces(hostConfig, "enclave-codex-abc-gateway", false)
	if hostConfig.NetworkMode != "container:enclave-codex-abc-gateway" {
		t.Fatalf("session must join the gateway network namespace, got %q", hostConfig.NetworkMode)
	}
	if hostConfig.UserNS != "" {
		t.Fatalf("docker sessions must not set --userns, got %q", hostConfig.UserNS)
	}

	usePodmanCLI(t)
	hostConfig = &dockercmd.HostConfig{}
	applyHostUserNamespace(hostConfig)
	joinGatewayNamespaces(hostConfig, "enclave-codex-abc-gateway", false)
	if hostConfig.UserNS != "container:enclave-codex-abc-gateway" {
		t.Fatalf("podman sessions must share the gateway user namespace, got %q", hostConfig.UserNS)
	}
}

func TestSessionsWithHostnameJoinGatewayUTSNamespaceOnlyUnderPodman(t *testing.T) {
	hostConfig := &dockercmd.HostConfig{}
	joinGatewayNamespaces(hostConfig, "enclave-codex-abc-gateway", true)
	if hostConfig.UTSMode != "" {
		t.Fatalf("docker shares the hostname with --network container: and rejects --uts container:, got %q", hostConfig.UTSMode)
	}

	usePodmanCLI(t)
	hostConfig = &dockercmd.HostConfig{}
	joinGatewayNamespaces(hostConfig, "enclave-codex-abc-gateway", false)
	if hostConfig.UTSMode != "" {
		t.Fatalf("sessions without a hostname must keep their UTS namespace, got %q", hostConfig.UTSMode)
	}
	joinGatewayNamespaces(hostConfig, "enclave-codex-abc-gateway", true)
	if hostConfig.UTSMode != "container:enclave-codex-abc-gateway" {
		t.Fatalf("podman sessions must share the gateway hostname, got %q", hostConfig.UTSMode)
	}
}

func TestApplyOCIRuntime(t *testing.T) {
	for _, tc := range []struct {
		name             string
		runtime          string
		context          ociRuntimeContext
		wantFlags        []string
		wantLabelDisable bool
	}{
		{name: "engine default", context: ociRuntimeContext{Rootless: true, SELinuxEnforcing: true, Admin: true}},
		{name: "runsc rootful", runtime: model.RuntimeRunsc, wantFlags: []string{"network=host"}},
		{name: "runsc rootless", runtime: model.RuntimeRunsc, context: ociRuntimeContext{Rootless: true}, wantFlags: []string{"network=host", "ignore-cgroups"}},
		{name: "runsc admin", runtime: model.RuntimeRunsc, context: ociRuntimeContext{Admin: true}, wantFlags: []string{"network=host", "allow-suid"}},
		{name: "runsc under SELinux", runtime: model.RuntimeRunsc, context: ociRuntimeContext{SELinuxEnforcing: true}, wantFlags: []string{"network=host"}, wantLabelDisable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hostConfig := &dockercmd.HostConfig{SecurityOpt: []string{"no-new-privileges"}}
			applyOCIRuntime(hostConfig, tc.runtime, tc.context)
			if hostConfig.Runtime != tc.runtime {
				t.Errorf("Runtime = %q, want %q", hostConfig.Runtime, tc.runtime)
			}
			if !slices.Equal(hostConfig.RuntimeFlags, tc.wantFlags) {
				t.Errorf("RuntimeFlags = %v, want %v", hostConfig.RuntimeFlags, tc.wantFlags)
			}
			if got := slices.Contains(hostConfig.SecurityOpt, "label=disable"); got != tc.wantLabelDisable {
				t.Errorf("label=disable in %v = %v, want %v", hostConfig.SecurityOpt, got, tc.wantLabelDisable)
			}
			if !slices.Contains(hostConfig.SecurityOpt, "no-new-privileges") {
				t.Errorf("existing security options were dropped: %v", hostConfig.SecurityOpt)
			}
		})
	}
}

// runsc cannot join another container's user or UTS namespace, so a runsc
// session shares only the gateway's network namespace.
func TestRunscSessionsJoinOnlyGatewayNetworkNamespace(t *testing.T) {
	usePodmanCLI(t)
	hostConfig := &dockercmd.HostConfig{}
	applyHostUserNamespace(hostConfig)
	applyOCIRuntime(hostConfig, model.RuntimeRunsc, ociRuntimeContext{Rootless: true})
	joinGatewayNamespaces(hostConfig, "enclave-codex-abc-gateway", true)
	if hostConfig.NetworkMode != "container:enclave-codex-abc-gateway" {
		t.Fatalf("runsc sessions must join the gateway network namespace, got %q", hostConfig.NetworkMode)
	}
	if hostConfig.UserNS != "keep-id" {
		t.Fatalf("runsc sessions must keep their keep-id user namespace, got %q", hostConfig.UserNS)
	}
	if hostConfig.UTSMode != "" {
		t.Fatalf("runsc sessions must keep their own UTS namespace, got %q", hostConfig.UTSMode)
	}
}

// Only the session container runs under the selected runtime; the gateway
// needs iptables and the keep-id prewarm only copies the image.
func TestRunscAppliesOnlyToSessionContainer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    backend.NetworkMode
		gateway bool
	}{
		{name: "restricted", mode: backend.NetworkModeRestricted, gateway: true},
		{name: "unrestricted", mode: backend.NetworkModeUnrestricted, gateway: true},
		{name: "without gateway", mode: backend.NetworkModeUnrestricted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := lockinHome(t)
			opts := lockinOptions(home)
			opts.Runtime = model.RuntimeRunsc
			b := New(opts)
			logPath := lockinEngine(t, backend.NamePodman)
			req := lockinRequest(prepareRunSession(t))
			dockerInfo = func(context.Context) (dockercmd.SystemInfo, error) {
				return dockercmd.SystemInfo{SecurityOptions: []string{"name=rootless"}}, nil
			}
			req.Network.Mode = tc.mode
			req.Network.Gateway = tc.gateway
			if tc.mode == backend.NetworkModeUnrestricted {
				req.Hostname = model.HostnamePrefix + "project"
			}

			if _, err := b.Start(context.Background(), req); err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			calls := lockinContainerCalls(t, logPath)
			gatewayName := req.Session.Name + "-gateway"
			session, ok := calls[req.Session.Name]
			if !ok {
				t.Fatalf("no session container was started: %v", calls)
			}
			want := [][]string{
				{"--runtime", model.RuntimeRunsc},
				{"--runtime-flag", "network=host"},
				{"--runtime-flag", "ignore-cgroups"},
				{"--userns", "keep-id"},
			}
			if tc.gateway {
				want = append(want, []string{"--network", "container:" + gatewayName})
			}
			if req.Hostname != "" {
				want = append(want, []string{"--hostname", req.Hostname})
			}
			for _, pair := range want {
				if !lockinHasArgPair(session, pair[0], pair[1]) {
					t.Errorf("session invocation lacks %s %s:\n%s", pair[0], pair[1], strings.Join(session, " "))
				}
			}
			if slices.Contains(session, "--uts") {
				t.Errorf("a runsc session must not join the gateway UTS namespace:\n%s", strings.Join(session, " "))
			}
			if lockinHasArgPair(session, "--runtime-flag", "allow-suid") {
				t.Errorf("a non-admin session must not honor setuid binaries:\n%s", strings.Join(session, " "))
			}
			if got := lockinHasArgPair(session, "--security-opt", "label=disable"); got != util.IsSELinuxEnforcing() {
				t.Errorf("session label=disable = %v, want %v (SELinux enforcing)", got, util.IsSELinuxEnforcing())
			}
			if _, ok := calls[gatewayName]; ok != tc.gateway {
				t.Fatalf("gateway container started = %v, want %v: %v", ok, tc.gateway, calls)
			}
			if !tc.gateway {
				i := slices.Index(session, "--network")
				if i < 0 || i+1 >= len(session) || strings.HasPrefix(session[i+1], "container:") {
					t.Errorf("a session without a gateway must attach to its own network:\n%s", strings.Join(session, " "))
				}
			}
			for name, args := range calls {
				if name == req.Session.Name {
					continue
				}
				if slices.Contains(args, "--runtime") || slices.Contains(args, "--runtime-flag") || slices.Contains(args, "label=disable") {
					t.Errorf("%s must use the engine's default runtime:\n%s", name, strings.Join(args, " "))
				}
			}
		})
	}
}
