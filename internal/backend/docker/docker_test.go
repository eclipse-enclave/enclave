// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
	"enclave/internal/util"
)

func TestDockerConfigBindMountsStoresFromExactStoreDirs(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	b := New(Options{Host: model.Host{Home: home, UID: "1000", GID: "1000"}})

	hash := "abc123abc123"
	req := backend.Request{
		Session: backend.SessionMeta{Tool: "codex", ProjectHash: hash, Name: "enclave-codex-" + hash},
		Image:   "enclave-test:latest",
		Stores: []backend.PersistentStore{
			{Kind: backend.StoreKindConfig, Key: backend.StoreKey{Owner: "codex", ProjectHash: hash}, ContainerPath: "/config"},
			{Kind: backend.StoreKindAuth, Key: backend.StoreKey{Owner: "codex"}, ContainerPath: "/auth", ReadOnly: true},
			{Kind: backend.StoreKindEnv, Key: backend.StoreKey{Owner: "codex", ProjectHash: hash}, ContainerPath: "/env"},
			{Kind: backend.StoreKindFeatureAuth, Key: backend.StoreKey{Owner: "playwright"}, ContainerPath: "/feature"},
		},
	}

	spec := b.dockerConfig(req)

	want := map[string]struct {
		source   string
		readOnly bool
	}{
		"/config":  {config.HostStoreConfigDir(home, "codex", hash, "default"), false},
		"/auth":    {config.HostStoreAuthDir(home, "codex", ""), true},
		"/env":     {config.HostStoreEnvDir(home, "codex", hash), false},
		"/feature": {config.HostStoreFeatureAuthDir(home, "playwright"), false},
	}

	got := map[string]dockercmd.Mount{}
	for _, m := range spec.hostConfig.Mounts {
		got[m.Target] = m
	}
	for target, expect := range want {
		m, ok := got[target]
		if !ok {
			t.Fatalf("no mount for %s; mounts=%v", target, spec.hostConfig.Mounts)
		}
		if m.Type != dockercmd.MountTypeBind {
			t.Fatalf("mount %s type = %q, want bind", target, m.Type)
		}
		if m.Source != expect.source {
			t.Fatalf("mount %s source = %q, want %q", target, m.Source, expect.source)
		}
		if m.ReadOnly != expect.readOnly {
			t.Fatalf("mount %s readOnly = %v, want %v", target, m.ReadOnly, expect.readOnly)
		}
		if _, err := os.Stat(m.Source); err != nil {
			t.Fatalf("mount source %s not created: %v", m.Source, err)
		}
	}
}

func TestDockerConfigSkipsMalformedStoreKey(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b := New(Options{Host: model.Host{Home: t.TempDir()}})

	req := backend.Request{
		Session: backend.SessionMeta{Tool: "codex"},
		Image:   "enclave-test:latest",
		Stores: []backend.PersistentStore{
			{Kind: backend.StoreKindConfig, Key: backend.StoreKey{Owner: "../evil", ProjectHash: "abc123abc123"}, ContainerPath: "/config"},
		},
	}

	spec := b.dockerConfig(req)
	for _, m := range spec.hostConfig.Mounts {
		if m.Target == "/config" {
			t.Fatalf("malformed store key produced a mount: %+v", m)
		}
	}
}

func TestLabelsFromRequestPreservesDisplaySessionName(t *testing.T) {
	labels := labelsFromRequest(backend.Request{
		Session: backend.SessionMeta{
			Tool:        "codex",
			ProjectHash: "abc123abc123",
			Worktree:    "/work/project",
			Name:        "enclave-codex-abc123abc123-feature-branch",
			DisplayName: "Feature Branch",
		},
	})

	if labels[model.LabelSession] != "Feature Branch" {
		t.Fatalf("expected raw display session label, got %q", labels[model.LabelSession])
	}
}

func TestLabelsFromRequestFallsBackToParsedSessionName(t *testing.T) {
	labels := labelsFromRequest(backend.Request{
		Session: backend.SessionMeta{
			Tool:        "codex",
			ProjectHash: "abc123abc123",
			Name:        "enclave-codex-abc123abc123-1",
		},
	})

	if labels[model.LabelSession] != "1" {
		t.Fatalf("expected parsed session label, got %q", labels[model.LabelSession])
	}
}

func TestLabelsFromRequestRecordsSessionMonitorOwner(t *testing.T) {
	labels := labelsFromRequest(backend.Request{
		Session: backend.SessionMeta{Tool: "claude", Name: "enclave-claude-abc123abc123-1"},
		Env: []backend.EnvVar{
			{Name: model.EnvSessionMonitor, Value: "1"},
			{Name: model.EnvSessionMonitorUser, Value: "agent"},
		},
	})

	if labels[model.LabelSessionMonitor] != "true" {
		t.Fatalf("expected session-monitor label, got %q", labels[model.LabelSessionMonitor])
	}
	if labels[model.LabelSessionMonitorUser] != "agent" {
		t.Fatalf("expected recorded tmux owner label, got %q", labels[model.LabelSessionMonitorUser])
	}
}

func TestLabelsFromRequestRecordsProjectDir(t *testing.T) {
	labels := labelsFromRequest(backend.Request{
		Session: backend.SessionMeta{
			Tool:         "codex",
			ProjectHash:  "abc123abc123",
			Worktree:     "relative/project",
			RealWorktree: "/resolved/project",
			Name:         "enclave-codex-abc123abc123",
		},
	})

	if labels[model.LabelWorktree] != "relative/project" {
		t.Fatalf("expected worktree label %q, got %q", "relative/project", labels[model.LabelWorktree])
	}
	if labels[model.LabelProjectDir] != "/resolved/project" {
		t.Fatalf("expected project-dir label %q, got %q", "/resolved/project", labels[model.LabelProjectDir])
	}
}

func TestSessionFromSummaryProjectDir(t *testing.T) {
	t.Run("uses project-dir label", func(t *testing.T) {
		session, ok := sessionFromSummary(dockercmd.Summary{
			Names: []string{"/enclave-codex-abc123abc123"},
			Labels: map[string]string{
				model.LabelAgent:      "codex",
				model.LabelWorktree:   "relative/project",
				model.LabelProjectDir: "/resolved/project",
			},
		})
		if !ok {
			t.Fatal("expected managed session")
		}
		if session.ProjectDir != "/resolved/project" {
			t.Fatalf("expected ProjectDir %q, got %q", "/resolved/project", session.ProjectDir)
		}
	})

	t.Run("falls back to worktree when project-dir label absent", func(t *testing.T) {
		session, ok := sessionFromSummary(dockercmd.Summary{
			Names: []string{"/enclave-codex-abc123abc123"},
			Labels: map[string]string{
				model.LabelAgent:    "codex",
				model.LabelWorktree: "/legacy/project",
			},
		})
		if !ok {
			t.Fatal("expected managed session")
		}
		if session.ProjectDir != "/legacy/project" {
			t.Fatalf("expected ProjectDir fallback %q, got %q", "/legacy/project", session.ProjectDir)
		}
	})
}

func TestPrepareRunAppliesRuntimeUIDRemapEnvAfterDevcontainerEnv(t *testing.T) {
	b := newDevcontainerBackend(t.TempDir(),
		"--env", model.EnvRuntimeUID+"=9999",
		"--env", model.EnvRuntimeGID+"=9999",
		"--env", "HOME=/home/devcontainer",
		"--env", "USER=devcontainer",
	)
	req := backend.Request{
		Session: backend.SessionMeta{
			Tool:        "codex",
			ProjectHash: "abc123abc123",
			Name:        "enclave-codex-abc123abc123",
		},
		Image: "enclave-test:latest",
		Env: []backend.EnvVar{
			{Name: model.EnvRuntimeUID, Value: "2000"},
			{Name: model.EnvRuntimeGID, Value: "3000"},
			{Name: "HOME", Value: model.ContainerHome},
			{Name: "USER", Value: model.ContainerUser},
		},
		Network:         backend.NetworkPolicy{Mode: backend.NetworkModeUnrestricted},
		RuntimeUIDRemap: true,
	}

	spec, err := b.prepareRun(context.Background(), req)
	if err != nil {
		t.Fatalf("prepareRun() error = %v", err)
	}

	for key, want := range map[string]string{
		model.EnvRuntimeUID: "2000",
		model.EnvRuntimeGID: "3000",
		"HOME":              model.ContainerHome,
		"USER":              model.ContainerUser,
	} {
		if got := lastEnvValue(spec.config.Env, key); got != want {
			t.Fatalf("last %s env = %q, want %q; env=%v", key, got, want, spec.config.Env)
		}
	}
}

func TestPrepareRunSkipsDevcontainerRunArgsForDetachedRequests(t *testing.T) {
	b := newDevcontainerBackend(t.TempDir(), "--env", "DEVCONTAINER_ONLY=1", "--hostname", "devhost")
	req := backend.Request{
		Session: backend.SessionMeta{
			Tool:        "codex",
			ProjectHash: "abc123abc123",
			Name:        "enclave-codex-abc123abc123-bg",
			Background:  true,
		},
		Image:    "enclave-test:latest",
		Network:  backend.NetworkPolicy{Mode: backend.NetworkModeUnrestricted},
		Detached: true,
	}

	spec, err := b.prepareRun(context.Background(), req)
	if err != nil {
		t.Fatalf("prepareRun() error = %v", err)
	}

	if got := lastEnvValue(spec.config.Env, "DEVCONTAINER_ONLY"); got != "" {
		t.Fatalf("detached run should not apply devcontainer env, got %q in %v", got, spec.config.Env)
	}
	if got := spec.config.Hostname; got == "devhost" {
		t.Fatalf("detached run should not apply devcontainer hostname, got %q", got)
	}
}

func TestStartRunsDetachedInteractiveContainer(t *testing.T) {
	logPath := installFakeDocker(t)
	ref, err := New(Options{}).Start(context.Background(), backend.Request{
		Session: backend.SessionMeta{
			Tool:        "codex",
			ProjectHash: "abc123abc123",
			Name:        "enclave-codex-abc123abc123-bg",
			Background:  true,
		},
		Image:    "enclave-test:latest",
		Network:  backend.NetworkPolicy{Mode: backend.NetworkModeUnrestricted},
		Detached: true,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if ref.ID != "fake-container-id" {
		t.Fatalf("ref.ID = %q, want fake-container-id", ref.ID)
	}

	args := readFakeDockerArgs(t, logPath)
	for _, want := range []string{"--detach", "--interactive", "--tty"} {
		if !containsArg(args, want) {
			t.Fatalf("docker run missing %s; args=%v", want, args)
		}
	}
}

func TestCheckPingsDockerDaemon(t *testing.T) {
	logPath := installFakeDocker(t)

	if err := New(Options{}).Check(context.Background()); err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	args := readFakeDockerArgs(t, logPath)
	for _, want := range []string{"version", "--format", "{{.Server.Version}}"} {
		if !containsArg(args, want) {
			t.Fatalf("docker ping missing %q; args=%v", want, args)
		}
	}
}

func TestSessionListFilterPairsScopeUnfilteredListToManagedContainers(t *testing.T) {
	got := sessionListFilterPairs(backend.SessionFilter{RunningOnly: true})
	want := [][2]string{
		{"label", model.LabelAgent},
		{"status", "running"},
	}
	if !filterPairsEqual(got, want) {
		t.Fatalf("sessionListFilterPairs() = %v, want %v", got, want)
	}
}

func TestSessionListFilterPairsLeaveNamePrefixUnscopedForLegacyNames(t *testing.T) {
	got := sessionListFilterPairs(backend.SessionFilter{All: true, NamePrefix: "enclave-codex-abc123abc123-"})
	for _, pair := range got {
		if pair[0] == "label" && pair[1] == model.LabelAgent {
			t.Fatalf("name-prefix scan must not require labels; got %v", got)
		}
	}
}

func TestSessionFromSummaryKeepsUserSessionNamedGateway(t *testing.T) {
	session, ok := sessionFromSummary(dockercmd.Summary{
		Names:  []string{"/enclave-codex-abc123abc123-gateway"},
		Image:  "enclave-codex:latest",
		Labels: map[string]string{model.LabelAgent: "codex", model.LabelHash: "abc123abc123", model.LabelSession: "gateway"},
	})

	if !ok {
		t.Fatal("expected user session named gateway to remain visible")
	}
	if session.Name != "gateway" {
		t.Fatalf("session name = %q, want gateway", session.Name)
	}
}

func TestSessionFromSummaryDropsGatewaySidecarByLabel(t *testing.T) {
	_, ok := sessionFromSummary(dockercmd.Summary{
		Names:  []string{"/enclave-codex-abc123abc123-gateway"},
		Image:  model.GatewayImagePrefix + "codex:latest",
		Labels: map[string]string{model.LabelAgent: "codex", model.GatewayLabelManaged: "true"},
	})
	if ok {
		t.Fatal("expected gateway sidecar to be hidden")
	}
}

func TestSessionPortMappingsSortsAndDropsUnbound(t *testing.T) {
	mappings := sessionPortMappings(dockercmd.PortMap{
		"8080/tcp": {{HostIP: "127.0.0.1", HostPort: "8080"}},
		"3000/tcp": {{HostIP: "127.0.0.1", HostPort: "3000"}},
		"9229/tcp": nil, // exposed but unpublished
	})

	if len(mappings) != 2 {
		t.Fatalf("expected 2 mappings, got %d: %v", len(mappings), mappings)
	}
	if mappings[0].ContainerPort != "3000" || mappings[1].ContainerPort != "8080" {
		t.Fatalf("expected mappings sorted by container port, got %v", mappings)
	}
	if mappings[0].HostIP != "127.0.0.1" || mappings[0].HostPort != "3000" || mappings[0].Protocol != "tcp" {
		t.Fatalf("unexpected first mapping %+v", mappings[0])
	}
}

// A host port of "0" is Docker's request for an OS-assigned port and must be
// preserved as a binding (so `--publish` lets the daemon pick a free port),
// while a genuinely empty host port stays dropped.
func TestPortMapKeepsAutoAssignedHostPort(t *testing.T) {
	bindings := portMap([]backend.PortMapping{
		{HostIP: "127.0.0.1", HostPort: "0", ContainerPort: "5391", Protocol: "tcp"},
		{HostIP: "127.0.0.1", HostPort: "", ContainerPort: "9229", Protocol: "tcp"},
	})

	auto := bindings["5391/tcp"]
	if len(auto) != 1 || auto[0].HostIP != "127.0.0.1" || auto[0].HostPort != "0" {
		t.Fatalf("auto-assigned binding = %+v, want one 127.0.0.1:0 binding", auto)
	}
	if _, ok := bindings["9229/tcp"]; ok {
		t.Fatalf("empty host port should be dropped, got %+v", bindings["9229/tcp"])
	}
}

func TestFillGatewayPortsReadsBindingFromGatewayContainer(t *testing.T) {
	var inspected []string
	containerInspectMany = func(_ context.Context, ids []string) ([]dockercmd.InspectResponse, error) {
		inspected = ids
		return []dockercmd.InspectResponse{{
			Name: "/enclave-codex-abc123abc123-main" + model.GatewayContainerSuffix,
			NetworkSettings: &dockercmd.NetworkSettings{
				Ports: dockercmd.PortMap{"3000/tcp": {{HostIP: "127.0.0.1", HostPort: "3000"}}},
			},
		}}, nil
	}
	t.Cleanup(func() { containerInspectMany = dockercmd.ContainerInspectMany })

	sessions := []backend.Session{
		{
			Ref:    backend.SessionRef{Name: "enclave-codex-abc123abc123-main"},
			Status: "running",
		},
		{
			Ref:    backend.SessionRef{Name: "enclave-claude-def456def456-main"},
			Status: "running",
			Ports:  []backend.PortMapping{{HostIP: "127.0.0.1", HostPort: "8080", ContainerPort: "8080", Protocol: "tcp"}},
		},
		{
			Ref:    backend.SessionRef{Name: "enclave-pi-def456def456-old"},
			Status: "exited",
		},
	}
	fillGatewayPorts(context.Background(), sessions)

	wantGateway := "enclave-codex-abc123abc123-main" + model.GatewayContainerSuffix
	if len(inspected) != 1 || inspected[0] != wantGateway {
		t.Fatalf("expected single gateway inspect for %q, got %v", wantGateway, inspected)
	}
	if len(sessions[0].Ports) != 1 || sessions[0].Ports[0].HostPort != "3000" {
		t.Fatalf("expected gateway binding on first session, got %v", sessions[0].Ports)
	}
	if sessions[1].Ports[0].HostPort != "8080" {
		t.Fatalf("expected tool-container binding to be kept, got %v", sessions[1].Ports)
	}
	if len(sessions[2].Ports) != 0 {
		t.Fatalf("expected no ports for exited session, got %v", sessions[2].Ports)
	}
}

func TestFillGatewayPortsToleratesInspectFailure(t *testing.T) {
	containerInspectMany = func(context.Context, []string) ([]dockercmd.InspectResponse, error) {
		return nil, errors.New("daemon unavailable")
	}
	t.Cleanup(func() { containerInspectMany = dockercmd.ContainerInspectMany })

	sessions := []backend.Session{{
		Ref:    backend.SessionRef{Name: "enclave-codex-abc123abc123-main"},
		Status: "running",
	}}
	fillGatewayPorts(context.Background(), sessions)

	if len(sessions[0].Ports) != 0 {
		t.Fatalf("expected no ports after inspect failure, got %v", sessions[0].Ports)
	}
}

func TestRemoveReturnsFinalizeErrorBeforeRemovingContainer(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.log")
	fakeDocker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + util.ShellQuote(logPath) + "\n" +
		"if [ \"$1\" = \"container\" ] && [ \"$2\" = \"inspect\" ]; then\n" +
		"  echo 'daemon unavailable' >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := New(Options{}).Remove(context.Background(), backend.SessionRef{Name: "managed"})
	if err == nil {
		t.Fatal("expected finalize error, got nil")
	}
	if !strings.Contains(err.Error(), "finalize auth before removing container managed") {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	if strings.Contains(string(raw), "\nrm ") || strings.HasPrefix(string(raw), "rm ") {
		t.Fatalf("docker rm should not run after finalize failure; log:\n%s", raw)
	}
}

func filterPairsEqual(a [][2]string, b [][2]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func lastEnvValue(env []string, key string) string {
	prefix := key + "="
	value := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			value = entry[len(prefix):]
		}
	}
	return value
}

func TestNeutralizeExitError(t *testing.T) {
	t.Parallel()

	if err := neutralizeExitError(nil); err != nil {
		t.Fatalf("neutralizeExitError(nil) = %v, want nil", err)
	}

	var exitErr *backend.ExitError
	err := neutralizeExitError(&dockercmd.ExitError{Code: 7})
	if !errors.As(err, &exitErr) {
		t.Fatalf("neutralizeExitError(docker exit error) = %T, want *backend.ExitError", err)
	}
	if exitErr.Code != 7 {
		t.Fatalf("neutralized exit code = %d, want 7", exitErr.Code)
	}

	plain := errors.New("boom")
	if err := neutralizeExitError(plain); err != plain {
		t.Fatalf("neutralizeExitError(plain) = %v, want the original error", err)
	}
}

// TestExecPreservesExitStatus guards the exec exit-code contract across the
// seam: the command's non-zero status must surface as *backend.ExitError so
// the CLI can exit with the same code.
func TestExecPreservesExitStatus(t *testing.T) {
	orig := execInteractive
	execInteractive = func(context.Context, string, []string, string, bool) error {
		return &dockercmd.ExitError{Code: 7}
	}
	t.Cleanup(func() { execInteractive = orig })

	b := New(Options{})
	err := b.Exec(context.Background(), backend.SessionRef{Name: "missing-session"}, backend.ExecRequest{Argv: []string{"true"}}, backend.AttachIO{})
	var exitErr *backend.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Exec error = %T (%v), want *backend.ExitError", err, err)
	}
	if exitErr.Code != 7 {
		t.Fatalf("Exec exit code = %d, want 7", exitErr.Code)
	}
}
