// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

const testMCPConfig = "/usr/local/share/enclave/templates/claude-a2a-mcp.json"

func peerGroupRuntime(role string, cmdArgs ...string) *Runtime {
	r := plainSessionRuntime(nil)
	r.profile.A2A.OrchestratorArgs = []string{"--mcp-config", testMCPConfig}
	r.run.PeerGroup = "review"
	r.run.A2ARole = role
	r.run.NoMemory = false
	r.run.CmdArgs = cmdArgs
	return r
}

func agentArgv(r *Runtime) []string {
	argv := newCommandBuilder(r).Build()
	return argv[4:]
}

func TestOrchestratorArgsFollowUserArgs(t *testing.T) {
	cases := map[string]struct {
		cmdArgs []string
		want    []string
	}{
		"prompt": {[]string{"plan the refactor"},
			[]string{"claude", "--dangerously-skip-permissions", "plan the refactor", "--mcp-config", testMCPConfig}},
		"resume picker": {[]string{"--resume"},
			[]string{"claude", "--dangerously-skip-permissions", "--resume", "--mcp-config", testMCPConfig}},
		"continue with prompt": {[]string{"--continue", "go on"},
			[]string{"claude", "--dangerously-skip-permissions", "--continue", "go on", "--mcp-config", testMCPConfig}},
		"terminator": {[]string{"--model", "x", "--", "-starts with dash"},
			[]string{"claude", "--dangerously-skip-permissions", "--model", "x", "--mcp-config", testMCPConfig, "--", "-starts with dash"}},
		"no args": {nil,
			[]string{"claude", "--dangerously-skip-permissions", "--mcp-config", testMCPConfig}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := agentArgv(peerGroupRuntime("orchestrator", tc.cmdArgs...))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("argv = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOrchestratorArgsOnlyForOrchestrators(t *testing.T) {
	worker := agentArgv(peerGroupRuntime("worker", "hi"))
	if slices.Contains(worker, "--mcp-config") {
		t.Fatalf("worker argv carries orchestrator args: %q", worker)
	}
	plain := peerGroupRuntime("orchestrator", "hi")
	plain.run.PeerGroup = ""
	if got := agentArgv(plain); slices.Contains(got, "--mcp-config") {
		t.Fatalf("session without --peer-group carries orchestrator args: %q", got)
	}
	shell := peerGroupRuntime("orchestrator")
	shell.run.Shell = true
	if got := newCommandBuilder(shell).Build(); slices.Contains(got, "--mcp-config") {
		t.Fatalf("shell argv carries orchestrator args: %q", got)
	}
}

func TestWorkerRunsForegroundWithoutTTY(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	be := &recordingBackend{}
	r := peerGroupRuntime("worker")
	r.backend = be
	ec := &ExecutionContext{ContainerName: "enclave-claude-abc123abc123-review", Env: []string{"TZ=UTC"}}
	if err := r.runContainer(context.Background(), ec, func() {}); err != nil {
		t.Fatal(err)
	}
	attach := be.attaches[0]
	if attach.TTY || attach.In != nil || attach.Out == nil || attach.Err == nil {
		t.Fatalf("worker attach = TTY %v, in %v, out set %v, err set %v; want no TTY, no stdin, both outputs",
			attach.TTY, attach.In != nil, attach.Out != nil, attach.Err != nil)
	}
	if backendEnvValue(be.requests[0].Env, "TERM") != "" {
		t.Fatalf("worker request carries the host TERM")
	}

	orchestrator := peerGroupRuntime("orchestrator")
	orchestrator.backend = be
	if err := orchestrator.runContainer(context.Background(), ec, func() {}); err != nil {
		t.Fatal(err)
	}
	if !be.attaches[1].TTY {
		t.Fatalf("orchestrator runs without a TTY")
	}
}

func TestJoinPeerGroupSetsRequestPeerGroup(t *testing.T) {
	r := peerGroupRuntime("worker")
	r.containerHome = "/home/agent"
	r.run.ProjectMount = model.ProjectMountReadonly
	session := &backend.PeerGroupSession{Group: "review", Member: "review"}
	var got PeerGroupMember
	r.SetPeerGroupJoiner(func(_ context.Context, m PeerGroupMember) (*backend.PeerGroupSession, error) {
		got = m
		return session, nil
	})
	key := backend.StoreKey{Owner: "claude", ProjectHash: "abc123abc123", Suffix: "review"}
	ec := &ExecutionContext{ContainerName: "enclave-claude-abc123abc123-review", ConfigStore: &backend.StoreRef{Kind: backend.StoreKindConfig, Key: key}}
	if err := r.joinPeerGroup(context.Background(), ec); err != nil {
		t.Fatal(err)
	}
	want := PeerGroupMember{
		SessionContainer:   "enclave-claude-abc123abc123-review",
		Member:             "review",
		ConfigStore:        key,
		ContainerConfigDir: "/home/agent/.claude",
		ProjectReadOnly:    true,
	}
	if got != want {
		t.Fatalf("member = %+v, want %+v", got, want)
	}
	if req := r.backendRequest(ec, true, true); req.PeerGroup != session {
		t.Fatalf("request PeerGroup = %v, want the joined session", req.PeerGroup)
	}
}

func TestJoinPeerGroupSkipsPlainSessionsAndReportsErrors(t *testing.T) {
	called := false
	plain := plainSessionRuntime(nil)
	plain.SetPeerGroupJoiner(func(context.Context, PeerGroupMember) (*backend.PeerGroupSession, error) {
		called = true
		return nil, nil
	})
	ec := &ExecutionContext{}
	if err := plain.joinPeerGroup(context.Background(), ec); err != nil || called || ec.PeerGroup != nil {
		t.Fatalf("plain session: err %v, joiner called %v, peer group %v", err, called, ec.PeerGroup)
	}

	failing := peerGroupRuntime("worker")
	failing.SetPeerGroupJoiner(func(context.Context, PeerGroupMember) (*backend.PeerGroupSession, error) {
		return nil, errors.New("name taken")
	})
	if err := failing.joinPeerGroup(context.Background(), ec); err == nil || err.Error() != "name taken" {
		t.Fatalf("err = %v, want the joiner's error", err)
	}
}

func TestA2AExtensionSkillsOnlyInPeerGroupSessions(t *testing.T) {
	for _, peerGroup := range []string{"", "review"} {
		t.Run("peer group "+peerGroup, func(t *testing.T) {
			home := t.TempDir()
			toolsDir := t.TempDir()
			builtin := filepath.Join(toolsDir, "claude", "skills")
			writeSkill(t, filepath.Join(builtin, "a2a-provenance"), "provenance")
			writeSkill(t, filepath.Join(builtin, "coding"), "coding")
			writeSkill(t, filepath.Join(config.HostSkillsDir(home), "a2a-mine"), "user skill")
			r := &Runtime{
				host:          model.Host{Home: home},
				project:       model.Project{Hash: "projhash"},
				profile:       model.Profile{Name: "claude", ConfigDir: ".claude", SkillsDir: ".claude/skills"},
				paths:         model.Paths{ToolsDir: toolsDir},
				containerHome: "/home/agent",
				run:           model.RunOptions{A2AOptions: model.A2AOptions{PeerGroup: peerGroup, A2ARole: "worker"}},
			}
			acc := newMountAccumulator(nil, nil)
			if err := r.addSkillMounts(acc); err != nil {
				t.Fatal(err)
			}
			source, ok := lookupMountSource(acc.Mounts(), "/home/agent/.claude/skills")
			if !ok {
				t.Fatal("no skills mount")
			}
			_, err := os.Stat(filepath.Join(source, "a2a-provenance"))
			if present := err == nil; present != (peerGroup != "") {
				t.Fatalf("a2a-provenance present = %v with peer group %q", present, peerGroup)
			}
			assertSkillBody(t, filepath.Join(source, "coding"), "coding")
			assertSkillBody(t, filepath.Join(source, "a2a-mine"), "user skill")

			target := t.TempDir()
			r.profile.SkillsDir = ".claude/skills"
			if err := r.applyExtensionSkillsLayer(target); err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(filepath.Join(target, "skills", "a2a-provenance"))
			if present := err == nil; present != (peerGroup != "") {
				t.Fatalf("config layer a2a-provenance present = %v with peer group %q", present, peerGroup)
			}
		})
	}
}
