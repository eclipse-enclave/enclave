// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

func TestSessionMemoryMountFollowsConfigStoreKey(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		run        model.RunOptions
		worktree   bool
		concurrent bool
		wantKey    string
	}{
		{name: "default", wantKey: "default"},
		{name: "named", run: model.RunOptions{SessionName: "review"}, wantKey: "review"},
		{name: "background", run: model.RunOptions{Background: true, SessionName: "worker"}, wantKey: "worker"},
		{name: "worktree", run: model.RunOptions{HostConfig: model.HostConfigPassthrough}, worktree: true},
		{name: "concurrent", concurrent: true, wantKey: "2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := model.Project{Dir: "/tmp/repo", RealDir: "/tmp/repo"}
			project.Hash = projectPathHash(project)
			if tc.worktree {
				project.Dir = "/tmp/repo-feature"
				project.RealDir = project.Dir
				tc.wantKey = projectPathHash(project)
			}
			r := &Runtime{
				host:          model.Host{Home: t.TempDir()},
				project:       project,
				run:           tc.run,
				profile:       model.Profile{Name: "custom", ConfigDir: ".custom", MemoryDir: ".custom/memories", MemoryScope: model.MemoryScopeSession},
				containerHome: "/home/agent",
				backend: &fakeBackend{configKeyInUse: func(backend.SessionMeta, string) (bool, error) {
					return tc.concurrent, nil
				}},
			}
			containerName := "enclave-custom-project"
			if tc.concurrent {
				containerName += "-2"
			}
			r.setConfigVolumeSuffix(containerName, "enclave-custom-project")
			acc := newMountAccumulator(nil, nil)
			r.addMemoryMounts(acc)
			got, ok := lookupMountSource(acc.Mounts(), "/home/agent/.custom/memories")
			want := filepath.Join(config.HostProjectMemoryDir(r.host.Home, project.Hash, "custom"), tc.wantKey)
			if !ok || got != want {
				t.Fatalf("memory mount = %q, want %q", got, want)
			}
		})
	}
}

func TestMemoryDisableArgsAtLaunch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		run      model.RunOptions
		disabled bool
	}{
		{name: "normal"},
		{name: "no-memory", run: model.RunOptions{NoMemory: true}, disabled: true},
		{name: "ephemeral", run: model.RunOptions{Ephemeral: true}, disabled: true},
		{name: "both", run: model.RunOptions{NoMemory: true, Ephemeral: true}, disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, args := range [][]string{nil, {"resume", "--last"}, {"exec", "a prompt"}} {
				r := &Runtime{
					profile: model.Profile{Name: "custom", Command: "custom", NoMemoryArgs: []string{"-c", "features.memories=false"}},
					run:     tc.run,
				}
				r.run.CmdArgs = args
				// Assert the complete command, including the wrapper's $0 argument.
				want := []string{"bash", "-c", agentWrapperTemplate, "bash", "custom"}
				if tc.disabled {
					want = append(want, "-c", "features.memories=false")
				}
				want = append(want, args...)
				if got := newCommandBuilder(r).Build(); !reflect.DeepEqual(got, want) {
					t.Fatalf("command = %q, want %q", got, want)
				}
				r.profile.NoMemoryArgs = nil
				want = append([]string{"bash", "-c", agentWrapperTemplate, "bash", "custom"}, args...)
				if got := newCommandBuilder(r).Build(); !reflect.DeepEqual(got, want) {
					t.Fatalf("tool without memory controls = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestAddMemoryMountsSkipsWhenMemoryDirUnset(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		host:          model.Host{Home: t.TempDir()},
		project:       model.Project{Hash: "projhash"},
		profile:       model.Profile{Name: "codex"},
		containerHome: "/home/agent",
	}
	acc := newMountAccumulator(nil, nil)
	r.addMemoryMounts(acc)
	if len(acc.Mounts()) != 0 {
		t.Fatalf("expected no mounts, got %d", len(acc.Mounts()))
	}
}

func TestAddMemoryMountsBindsDirectory(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	projectHash := "projhash"
	r := &Runtime{
		host:          model.Host{Home: home},
		project:       model.Project{Hash: projectHash},
		profile:       model.Profile{Name: "claude", MemoryDir: ".claude/memory"},
		containerHome: "/home/agent",
	}

	acc := newMountAccumulator(nil, nil)
	r.addMemoryMounts(acc)

	target := "/home/agent/.claude/memory"
	source, ok := lookupMountSource(acc.Mounts(), target)
	if !ok {
		t.Fatalf("expected memory mount at %s", target)
	}
	if want := config.HostProjectMemoryDir(home, projectHash, "claude"); source != want {
		t.Fatalf("memory mount source = %q, want %q", source, want)
	}
	for _, m := range acc.Mounts() {
		if m.ContainerPath == target && m.ReadOnly {
			t.Fatalf("expected memory mount to be writable")
		}
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("expected host memory directory to exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %s to be a directory", source)
	}
}

func TestAddMemoryMountsRespectsNoMemory(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		host:          model.Host{Home: t.TempDir()},
		project:       model.Project{Hash: "projhash"},
		profile:       model.Profile{Name: "claude", MemoryDir: ".claude/memory"},
		containerHome: "/home/agent",
		run:           model.RunOptions{NoMemory: true},
	}

	acc := newMountAccumulator(nil, nil)
	r.addMemoryMounts(acc)
	if len(acc.Mounts()) != 0 {
		t.Fatalf("expected no mounts when NoMemory is set, got %d", len(acc.Mounts()))
	}
}

func TestAddMemoryMountsRespectsEphemeral(t *testing.T) {
	t.Parallel()

	r := &Runtime{
		host:          model.Host{Home: t.TempDir()},
		project:       model.Project{Hash: "projhash"},
		profile:       model.Profile{Name: "claude", MemoryDir: ".claude/memory"},
		containerHome: "/home/agent",
		run:           model.RunOptions{Ephemeral: true},
	}

	acc := newMountAccumulator(nil, nil)
	r.addMemoryMounts(acc)
	if len(acc.Mounts()) != 0 {
		t.Fatalf("expected no mounts for ephemeral session, got %d", len(acc.Mounts()))
	}
}
