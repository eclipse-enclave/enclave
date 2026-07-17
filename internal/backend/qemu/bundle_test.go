// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package qemu

import (
	"path/filepath"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/model"
)

func TestResolveBundleMemoryMiBDefaultsTo4096(t *testing.T) {
	got, err := resolveBundleMemoryMiB(t.TempDir())
	if err != nil {
		t.Fatalf("resolveBundleMemoryMiB: %v", err)
	}
	if DefaultMemoryMiB != 4096 {
		t.Fatalf("DefaultMemoryMiB = %d, want 4096", DefaultMemoryMiB)
	}
	if got != DefaultMemoryMiB {
		t.Fatalf("default memory = %d, want %d", got, DefaultMemoryMiB)
	}
}

func TestBuildRuntimeMountsPropagatesStoreCacheMmap(t *testing.T) {
	be := New(Options{Host: model.Host{Home: t.TempDir()}})
	projectDir := t.TempDir()
	controlDir := filepath.Join(t.TempDir(), "control")
	req := backend.Request{
		Session: backend.SessionMeta{Tool: "codex"},
		Mounts: []backend.Mount{{
			Type:          backend.MountTypeBind,
			Source:        projectDir,
			ContainerPath: "/workspace",
		}},
		Stores: []backend.PersistentStore{
			{
				Kind:          backend.StoreKindConfig,
				Key:           backend.StoreKey{Owner: "codex", ProjectHash: "project", Suffix: "default"},
				ContainerPath: "/home/agent/.codex",
				CacheMmap:     true,
			},
			{
				Kind:          backend.StoreKindAuth,
				Key:           backend.StoreKey{Owner: "codex"},
				ContainerPath: "/home/agent/.enclave-auth",
			},
		},
	}

	mounts, err := be.buildRuntimeMounts(req, controlDir, "")
	if err != nil {
		t.Fatalf("buildRuntimeMounts: %v", err)
	}

	if got := findRuntimeMount(t, mounts, "/workspace").CacheMmap; got {
		t.Fatal("workspace mount unexpectedly uses cache=mmap")
	}
	if got := findRuntimeMount(t, mounts, "/home/agent/.codex").CacheMmap; !got {
		t.Fatal("store with CacheMmap should use cache=mmap")
	}
	if got := findRuntimeMount(t, mounts, "/home/agent/.enclave-auth").CacheMmap; got {
		t.Fatal("store without CacheMmap unexpectedly uses cache=mmap")
	}
	if got := findRuntimeMount(t, mounts, guestControlPath).CacheMmap; got {
		t.Fatal("control mount unexpectedly uses cache=mmap")
	}
}

func findRuntimeMount(t *testing.T, mounts []runtimeMount, target string) runtimeMount {
	t.Helper()
	for _, mount := range mounts {
		if mount.Target == target {
			return mount
		}
	}
	t.Fatalf("runtime mount %s not found in %#v", target, mounts)
	return runtimeMount{}
}
