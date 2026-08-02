// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package docker

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"enclave/internal/backend"
	dockercmd "enclave/internal/docker"
	"enclave/internal/model"
)

func stubRootless(t *testing.T, rootless bool) {
	t.Helper()
	orig := isRootlessDocker
	isRootlessDocker = func(context.Context) (bool, error) { return rootless, nil }
	t.Cleanup(func() { isRootlessDocker = orig })
}

func sandboxTestBackend(t *testing.T) *Backend {
	t.Helper()
	return New(Options{Host: model.Host{Home: t.TempDir(), UID: "1000", GID: "1000"}})
}

func sandboxTestSpec() runSpec {
	return runSpec{config: &dockercmd.ContainerConfig{}, hostConfig: &dockercmd.HostConfig{}}
}

func TestApplyRootlessSandboxConfiguresLauncher(t *testing.T) {
	stubRootless(t, true)
	b := sandboxTestBackend(t)
	spec := sandboxTestSpec()

	if err := b.applyRootlessSandbox(context.Background(), backend.Request{}, spec); err != nil {
		t.Fatalf("apply rootless sandbox: %v", err)
	}
	if spec.config.User != "root" {
		t.Fatalf("expected launcher user root, got %q", spec.config.User)
	}
	for _, want := range []string{
		model.EnvRootlessSandbox + "=1",
		model.EnvSandboxUID + "=1000",
		model.EnvSandboxGID + "=1000",
	} {
		if !slices.Contains(spec.config.Env, want) {
			t.Fatalf("expected env %q, got %v", want, spec.config.Env)
		}
	}
	var seccompOpt string
	for _, opt := range spec.hostConfig.SecurityOpt {
		if strings.HasPrefix(opt, "seccomp=") {
			seccompOpt = opt
		}
	}
	if seccompOpt == "" {
		t.Fatalf("expected seccomp security opt, got %v", spec.hostConfig.SecurityOpt)
	}
	profilePath := strings.TrimPrefix(seccompOpt, "seccomp=")
	if !strings.HasPrefix(profilePath, filepath.Join(b.opts.Host.Home)) {
		t.Fatalf("expected profile under host home, got %s", profilePath)
	}
}

func TestApplyRootlessSandboxSkipsRootfulDaemon(t *testing.T) {
	stubRootless(t, false)
	b := sandboxTestBackend(t)
	spec := sandboxTestSpec()
	if err := b.applyRootlessSandbox(context.Background(), backend.Request{}, spec); err != nil {
		t.Fatalf("apply rootless sandbox: %v", err)
	}
	if spec.config.User != "" || len(spec.config.Env) != 0 || len(spec.hostConfig.SecurityOpt) != 0 {
		t.Fatalf("expected untouched spec on rootful daemon: %+v %+v", spec.config, spec.hostConfig)
	}
}

func TestApplyRootlessSandboxSkipsAdminSessions(t *testing.T) {
	stubRootless(t, true)
	b := sandboxTestBackend(t)
	spec := sandboxTestSpec()
	req := backend.Request{Security: backend.SecurityPosture{Admin: true}}
	if err := b.applyRootlessSandbox(context.Background(), req, spec); err != nil {
		t.Fatalf("apply rootless sandbox: %v", err)
	}
	if spec.config.User != "" || len(spec.hostConfig.SecurityOpt) != 0 {
		t.Fatalf("expected untouched spec for admin session: %+v", spec.config)
	}
}

func TestApplyRootlessSandboxRejectsRuntimeUIDRemap(t *testing.T) {
	stubRootless(t, true)
	b := sandboxTestBackend(t)
	if err := b.applyRootlessSandbox(context.Background(), backend.Request{RuntimeUIDRemap: true}, sandboxTestSpec()); err == nil {
		t.Fatalf("expected error for runtime uid remap under rootless")
	}
}

func TestApplyRootlessSandboxRejectsCustomUser(t *testing.T) {
	stubRootless(t, true)
	b := sandboxTestBackend(t)
	spec := sandboxTestSpec()
	spec.config.User = "vscode"
	if err := b.applyRootlessSandbox(context.Background(), backend.Request{}, spec); err == nil {
		t.Fatalf("expected error for custom container user under rootless")
	}
}

func TestWrapSandboxExec(t *testing.T) {
	stubRootless(t, true)
	b := sandboxTestBackend(t)
	argv, user := b.wrapSandboxExec(context.Background(), []string{"bash", "-lc", "id"}, "")
	if user != "root" {
		t.Fatalf("expected wrapper exec user root, got %q", user)
	}
	if len(argv) != 4 || argv[0] != sandboxExecPath || argv[1] != "bash" {
		t.Fatalf("unexpected wrapped argv: %v", argv)
	}

	// Admin execs stay in the outer namespace.
	argv, user = b.wrapSandboxExec(context.Background(), []string{"bash"}, "root")
	if user != "root" || argv[0] != "bash" {
		t.Fatalf("expected admin exec unwrapped, got %v as %q", argv, user)
	}
}

func TestWrapSandboxExecRootfulUnchanged(t *testing.T) {
	stubRootless(t, false)
	b := sandboxTestBackend(t)
	argv, user := b.wrapSandboxExec(context.Background(), []string{"bash"}, "")
	if user != "" || argv[0] != "bash" {
		t.Fatalf("expected rootful exec unwrapped, got %v as %q", argv, user)
	}
}

func TestSandboxIdentityFallsBackForRootInvoker(t *testing.T) {
	uid, gid := sandboxIdentity(model.Host{UID: "0", GID: "0"})
	if uid != "1000" || gid != "1000" {
		t.Fatalf("expected 1000:1000 fallback, got %s:%s", uid, gid)
	}
	uid, gid = sandboxIdentity(model.Host{UID: "1001", GID: "1002"})
	if uid != "1001" || gid != "1002" {
		t.Fatalf("expected host identity 1001:1002, got %s:%s", uid, gid)
	}
}

// TestRootlessSeccompProfile guards the embedded profile's delta: the launcher
// depends on exactly these syscalls being allowed, and the profile must stay a
// deny-by-default derivative of the Moby profile.
func TestRootlessSeccompProfile(t *testing.T) {
	var profile struct {
		DefaultAction string `json:"defaultAction"`
		Syscalls      []struct {
			Names  []string `json:"names"`
			Action string   `json:"action"`
			Args   []struct {
				Index    int    `json:"index"`
				Value    uint64 `json:"value"`
				ValueTwo uint64 `json:"valueTwo"`
				Op       string `json:"op"`
			} `json:"args"`
			Includes map[string]any `json:"includes"`
			Excludes map[string]any `json:"excludes"`
		} `json:"syscalls"`
	}
	if err := json.Unmarshal(rootlessSeccompProfile, &profile); err != nil {
		t.Fatalf("embedded seccomp profile is not valid JSON: %v", err)
	}
	if profile.DefaultAction == "SCMP_ACT_ALLOW" {
		t.Fatalf("profile must stay deny-by-default, got %s", profile.DefaultAction)
	}

	unconditional := map[string]bool{}
	unshareFiltered := false
	const forbiddenMask = ^uint64(0x10000000 | 0x00020000) // ~(CLONE_NEWUSER|CLONE_NEWNS)
	for _, rule := range profile.Syscalls {
		if rule.Action != "SCMP_ACT_ALLOW" || len(rule.Includes) > 0 || len(rule.Excludes) > 0 {
			continue
		}
		if len(rule.Args) == 0 {
			for _, name := range rule.Names {
				unconditional[name] = true
			}
			continue
		}
		if slices.Contains(rule.Names, "unshare") {
			for _, arg := range rule.Args {
				if arg.Index == 0 && arg.Op == "SCMP_CMP_MASKED_EQ" && arg.Value == forbiddenMask && arg.ValueTwo == 0 {
					unshareFiltered = true
				}
			}
		}
	}
	for _, name := range []string{"mount", "umount2", "mount_setattr", "open_tree", "move_mount", "setns"} {
		if !unconditional[name] {
			t.Fatalf("expected %s in the profile delta", name)
		}
	}
	if !unshareFiltered {
		t.Fatalf("expected unshare restricted to CLONE_NEWUSER|CLONE_NEWNS")
	}
	if unconditional["unshare"] {
		t.Fatalf("unshare must not be allowed unconditionally")
	}
}
