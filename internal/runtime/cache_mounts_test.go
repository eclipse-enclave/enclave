// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/backend"
	"enclave/internal/config"
	"enclave/internal/model"
)

func cacheTestRuntime(home string) *Runtime {
	return &Runtime{
		host:          model.Host{Home: home},
		project:       model.Project{Hash: "project-hash"},
		profile:       model.Profile{Name: "demo"},
		containerHome: "/home/agent",
	}
}

func findMountByTarget(mounts []backend.Mount, target string) (backend.Mount, bool) {
	for _, m := range mounts {
		if m.ContainerPath == target {
			return m, true
		}
	}
	return backend.Mount{}, false
}

// TestAddCacheMountsIncludesExtensionCaches covers tool- and mixin-declared
// caches joining the built-in mounts.
func TestAddCacheMountsIncludesExtensionCaches(t *testing.T) {
	r := cacheTestRuntime(t.TempDir())
	r.profile.Caches = []model.CacheConfig{{Name: "demo-cache", Target: ".cache/demo"}}
	r.features = []model.Extension{
		{
			Name:   "java-dev",
			Type:   model.ExtensionKindMixin,
			Caches: []model.CacheConfig{{Name: "m2", Target: ".m2/repository"}},
		},
	}

	mounts := newMountAccumulator(nil, nil)
	if err := r.addCacheMounts(mounts); err != nil {
		t.Fatalf("addCacheMounts: %v", err)
	}

	cacheDir := config.HostCacheToolProjectDir(r.host.Home, "demo", "project-hash")
	for target, name := range map[string]string{
		"/home/agent/.npm":           "npm", // built-in entries stay mounted
		"/home/agent/.cache/demo":    "demo-cache",
		"/home/agent/.m2/repository": "m2",
	} {
		mount, ok := findMountByTarget(mounts.Mounts(), target)
		if !ok {
			t.Fatalf("expected a cache mount at %s, got %v", target, mounts.Mounts())
		}
		if want := filepath.Join(cacheDir, name); mount.Source != want {
			t.Errorf("cache %q source = %q, want %q", name, mount.Source, want)
		}
		if mount.ReadOnly {
			t.Errorf("cache %q mounted read-only", name)
		}
	}
}

// TestAddCacheMountsSkipsExtensionCachesWithoutCache covers --no-cache also
// disabling extension-declared caches.
func TestAddCacheMountsSkipsExtensionCachesWithoutCache(t *testing.T) {
	r := cacheTestRuntime(t.TempDir())
	r.run.NoCache = true
	r.features = []model.Extension{
		{Name: "java-dev", Caches: []model.CacheConfig{{Name: "m2", Target: ".m2/repository"}}},
	}

	mounts := newMountAccumulator(nil, nil)
	if err := r.addCacheMounts(mounts); err != nil {
		t.Fatalf("addCacheMounts: %v", err)
	}
	if len(mounts.Mounts()) != 0 {
		t.Fatalf("did not expect cache mounts with cache disabled, got %v", mounts.Mounts())
	}
}

// TestProjectCachesDedupesIdenticalDeclarations covers an identical
// name+target pair from two features collapsing into one mount.
func TestProjectCachesDedupesIdenticalDeclarations(t *testing.T) {
	r := cacheTestRuntime(t.TempDir())
	cache := model.CacheConfig{Name: "m2", Target: ".m2/repository"}
	r.features = []model.Extension{
		{Name: "java-dev", Caches: []model.CacheConfig{cache}},
		{Name: "maven-tools", Caches: []model.CacheConfig{cache}},
	}

	caches, err := r.projectCaches()
	if err != nil {
		t.Fatalf("projectCaches: %v", err)
	}
	if want := len(model.BuiltinProjectCaches) + 1; len(caches) != want {
		t.Fatalf("len(caches) = %d, want %d (identical declarations dedupe): %v", len(caches), want, caches)
	}
}

// TestProjectCachesRejectsNameConflict covers one cache name with two
// different targets: the error names both claimants.
func TestProjectCachesRejectsNameConflict(t *testing.T) {
	r := cacheTestRuntime(t.TempDir())
	r.features = []model.Extension{
		{Name: "java-dev", Caches: []model.CacheConfig{{Name: "m2", Target: ".m2/repository"}}},
		{Name: "maven-tools", Caches: []model.CacheConfig{{Name: "m2", Target: ".m2"}}},
	}

	_, err := r.projectCaches()
	if err == nil {
		t.Fatal("expected a name-conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "java-dev") || !strings.Contains(err.Error(), "maven-tools") {
		t.Fatalf("err = %v, want it to name both claimants", err)
	}
}

// TestProjectCachesRejectsTargetConflict covers two cache names claiming
// overlapping targets, which would silently shadow one mount.
func TestProjectCachesRejectsTargetConflict(t *testing.T) {
	r := cacheTestRuntime(t.TempDir())
	r.profile.Caches = []model.CacheConfig{{Name: "tool-cache", Target: ".m2"}}
	r.features = []model.Extension{
		{Name: "java-dev", Caches: []model.CacheConfig{{Name: "m2", Target: ".m2/repository"}}},
	}

	_, err := r.projectCaches()
	if err == nil {
		t.Fatal("expected a target-conflict error, got nil")
	}
	if !strings.Contains(err.Error(), ".m2") || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("err = %v, want an overlap error naming the contested target", err)
	}
}

// TestProjectCachesRejectsProfileMountConflict covers a cache target inside
// the tool's own config store mount, which would otherwise fail at container
// start with a raw duplicate/nested mount error.
func TestProjectCachesRejectsProfileMountConflict(t *testing.T) {
	r := cacheTestRuntime(t.TempDir())
	r.profile.ConfigDir = ".demo"
	r.features = []model.Extension{
		{Name: "demo-ext", Caches: []model.CacheConfig{{Name: "demo-state", Target: ".demo/state"}}},
	}

	_, err := r.projectCaches()
	if err == nil {
		t.Fatal("expected a config-store conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "config store") {
		t.Fatalf("err = %v, want it to name the tool config store mount", err)
	}
}
