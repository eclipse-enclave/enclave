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
	"strings"
	"testing"

	"enclave/internal/config"
	"enclave/internal/model"
)

func newCacheMountRuntime(home string) *Runtime {
	return &Runtime{
		host:          model.Host{Home: home},
		project:       model.Project{Hash: "projhash"},
		profile:       model.Profile{Name: "claude"},
		containerHome: "/home/agent",
	}
}

// Regression for macOS cache deletion: every package-cache source must be
// created as a directory before use and mounted as a disposable,
// source-creatable directory, so a deleted cache root costs performance only.
func TestAddCacheMountsCreatesDisposableSources(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := newCacheMountRuntime(home)
	acc := newMountAccumulator(nil, nil)
	if err := r.addCacheMounts(acc); err != nil {
		t.Fatalf("addCacheMounts() error: %v", err)
	}

	wantCount := len(packageCacheDirs(r.containerHome))
	if len(acc.Mounts()) != wantCount {
		t.Fatalf("mounts = %d, want %d", len(acc.Mounts()), wantCount)
	}
	cacheRoot := config.HostCacheToolProjectDir(home, "claude", "projhash")
	for _, m := range acc.Mounts() {
		if !m.CreateSourceDir {
			t.Errorf("cache mount %s -> %s is not marked CreateSourceDir", m.Source, m.ContainerPath)
		}
		if m.ReadOnly {
			t.Errorf("cache mount %s must be writable", m.ContainerPath)
		}
		if filepath.Dir(m.Source) != cacheRoot {
			t.Errorf("cache mount source %s is outside the project cache root %s", m.Source, cacheRoot)
		}
		info, err := os.Stat(m.Source)
		if err != nil {
			t.Errorf("cache source %s was not created: %v", m.Source, err)
		} else if !info.IsDir() {
			t.Errorf("cache source %s is not a directory", m.Source)
		}
	}
	if source, ok := lookupMountSource(acc.Mounts(), "/home/agent/.npm"); !ok || source != filepath.Join(cacheRoot, "npm") {
		t.Errorf("npm cache mount source = %q, ok = %v", source, ok)
	}
}

func TestAddCacheMountsRespectsNoCache(t *testing.T) {
	t.Parallel()

	r := newCacheMountRuntime(t.TempDir())
	r.run = model.RunOptions{NoCache: true}
	acc := newMountAccumulator(nil, nil)
	if err := r.addCacheMounts(acc); err != nil {
		t.Fatalf("addCacheMounts() error: %v", err)
	}
	if len(acc.Mounts()) != 0 {
		t.Fatalf("expected no mounts with NoCache, got %d", len(acc.Mounts()))
	}
}

// Host-side creation failures must propagate instead of deferring to a
// container-start error the user cannot attribute.
func TestAddCacheMountsPropagatesCreationFailure(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := newCacheMountRuntime(home)
	cacheRoot := config.HostCacheToolProjectDir(home, "claude", "projhash")
	if err := os.MkdirAll(filepath.Dir(cacheRoot), 0o700); err != nil {
		t.Fatalf("mkdir cache parent: %v", err)
	}
	// A regular file where the cache tree belongs makes every MkdirAll fail.
	if err := os.WriteFile(cacheRoot, []byte(""), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	acc := newMountAccumulator(nil, nil)
	err := r.addCacheMounts(acc)
	if err == nil {
		t.Fatal("addCacheMounts() succeeded despite uncreatable cache directory")
	}
	if !strings.Contains(err.Error(), "package cache") {
		t.Errorf("error %q does not mention the package cache", err)
	}
}

// Required mounts must stay strict: only disposable cache directories may
// carry the source-creating marker.
func TestOnlyDisposableMountsCreateSources(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	r := newCacheMountRuntime(home)
	r.profile.MemoryDir = ".claude/memory"
	r.run.ImageInbox = true

	acc := newMountAccumulator(nil, nil)
	r.addMemoryMounts(acc)
	r.addHistoryMounts(acc)
	r.addImageInboxMount(acc)
	if err := r.addCacheMounts(acc); err != nil {
		t.Fatalf("addCacheMounts() error: %v", err)
	}

	cacheRoot := config.HostCacheDir(home)
	for _, m := range acc.Mounts() {
		underCacheRoot := strings.HasPrefix(m.Source, cacheRoot+string(filepath.Separator))
		if m.CreateSourceDir != underCacheRoot {
			t.Errorf("mount %s -> %s: CreateSourceDir = %v, want %v (disposable == under cache root)",
				m.Source, m.ContainerPath, m.CreateSourceDir, underCacheRoot)
		}
	}
}
