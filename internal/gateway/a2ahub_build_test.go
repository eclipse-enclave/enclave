// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package gateway

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"enclave/internal/docker"
	"enclave/internal/model"
)

func a2aHubRepoRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate A2A hub build test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

// requireA2AHubSources skips until the hub binary (A3) is on this branch.
func requireA2AHubSources(t *testing.T, repoRoot string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(repoRoot, "cmd", "enclave-a2a-hub")); errors.Is(err, os.ErrNotExist) {
		t.Skip("cmd/enclave-a2a-hub is not on this branch yet")
	}
}

func TestA2AHubBuildInputsPathsExist(t *testing.T) {
	repoRoot := a2aHubRepoRoot(t)
	requireA2AHubSources(t, repoRoot)
	for _, relPath := range a2aHubBuildPaths() {
		if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(relPath))); err != nil {
			t.Fatalf("A2A hub build input %s missing from repo: %v", relPath, err)
		}
	}
}

// The hub build stages only the manifest, so a missing package builds from the repo but not from packaged assets.
func TestA2AHubBuildInputsCoverInternalDeps(t *testing.T) {
	repoRoot := a2aHubRepoRoot(t)
	requireA2AHubSources(t, repoRoot)
	cmd := exec.Command("go", "list", "-deps", "./cmd/enclave-a2a-hub")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		relDir, ok := strings.CutPrefix(pkg, "enclave/")
		if !ok || !strings.HasPrefix(relDir, "internal/") {
			continue
		}
		covered := slices.ContainsFunc(a2aHubBuildInputs, func(entry string) bool {
			return relDir == entry || strings.HasPrefix(relDir, entry+"/")
		})
		if !covered {
			t.Fatalf("A2A hub depends on %s but a2ahub_build_inputs.txt does not stage it; add %q", pkg, relDir)
		}
	}
}

func TestA2AHubOnlyInputsStayOutOfGatewayHash(t *testing.T) {
	for _, entry := range []string{"cmd/enclave-a2a-hub", "internal/a2a", "internal/a2ahub", "internal/peergroup"} {
		if !slices.Contains(a2aHubBuildInputs, entry) {
			t.Fatalf("%s is not an A2A hub build input", entry)
		}
		if slices.Contains(gatewayProxyBuildInputs, entry) {
			t.Fatalf("%s is a gateway build input; hub code changes would rebuild every gateway image", entry)
		}
	}
}

// writeA2AHubAppRoot creates a minimal app root holding every hub build input.
func writeA2AHubAppRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		a2aHubDockerfileRel:                       "FROM scratch\n",
		a2aHubEntrypointRel:                       "#!/bin/sh\n",
		"go.mod":                                  "module enclave\n",
		"go.sum":                                  "",
		"cmd/enclave-a2a-hub/main.go":             "package main\n",
		"cmd/enclave-a2a-hub/main_test.go":        "package main\n",
		"internal/a2a/wire.go":                    "package a2a\n",
		"internal/a2ahub/proxy.go":                "package a2ahub\n",
		"internal/peergroup/snapshot.go":          "package peergroup\n",
		"internal/peergroup/testdata/fixture.txt": "x\n",
		"internal/config/unrelated.go":            "package config\n",
	}
	for _, entry := range a2aHubBuildInputs {
		if !strings.HasSuffix(entry, ".mod") && !strings.HasSuffix(entry, ".sum") {
			files[entry+"/doc.go"] = "package " + filepath.Base(entry) + "\n"
		}
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestA2AHubBuildHashTracksInputs(t *testing.T) {
	root := writeA2AHubAppRoot(t)
	base, err := a2aHubBuildHash(root)
	if err != nil {
		t.Fatalf("a2aHubBuildHash: %v", err)
	}
	cases := []struct {
		rel     string
		changes bool
	}{
		{rel: a2aHubDockerfileRel, changes: true},
		{rel: a2aHubEntrypointRel, changes: true},
		{rel: "go.sum", changes: true},
		{rel: "internal/a2ahub/proxy.go", changes: true},
		{rel: "internal/peergroup/snapshot.go", changes: true},
		{rel: "cmd/enclave-a2a-hub/main_test.go", changes: false},
		{rel: "internal/config/unrelated.go", changes: false},
	}
	for _, tc := range cases {
		path := filepath.Join(root, filepath.FromSlash(tc.rel))
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(original, []byte("// changed\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		changed, err := a2aHubBuildHash(root)
		if err != nil {
			t.Fatalf("a2aHubBuildHash after changing %s: %v", tc.rel, err)
		}
		if (changed != base) != tc.changes {
			t.Fatalf("changing %s: hash changed = %v, want %v", tc.rel, changed != base, tc.changes)
		}
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestA2AHubBuildHashFailsOnMissingInput(t *testing.T) {
	root := writeA2AHubAppRoot(t)
	if err := os.RemoveAll(filepath.Join(root, "internal", "a2ahub")); err != nil {
		t.Fatal(err)
	}
	if _, err := a2aHubBuildHash(root); err == nil || !strings.Contains(err.Error(), "internal/a2ahub") {
		t.Fatalf("a2aHubBuildHash error = %v, want missing internal/a2ahub", err)
	}
}

func TestPrepareA2AHubContextStagesOnlyHubInputs(t *testing.T) {
	root := writeA2AHubAppRoot(t)
	contextDir, cleanup, err := prepareA2AHubContext(root)
	if err != nil {
		t.Fatalf("prepareA2AHubContext: %v", err)
	}
	var staged []string
	err = filepath.WalkDir(contextDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel, _ := filepath.Rel(contextDir, path)
		staged = append(staged, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{a2aHubDockerfileRel, a2aHubEntrypointRel, "go.mod", "cmd/enclave-a2a-hub/main.go", "internal/peergroup/testdata/fixture.txt"} {
		if !slices.Contains(staged, want) {
			t.Fatalf("staged context %v lacks %s", staged, want)
		}
	}
	if slices.Contains(staged, "internal/config/unrelated.go") {
		t.Fatalf("staged context %v includes a non-hub package", staged)
	}
	cleanup()
	if _, err := os.Stat(contextDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup left %s: %v", contextDir, err)
	}
}

func stubA2AHubDocker(t *testing.T, labels map[string]string, exists bool) *[]docker.BuildRequest {
	t.Helper()
	var builds []docker.BuildRequest
	inspect, existsFn, build := a2aHubImageInspect, a2aHubImageExists, a2aHubImageBuild
	t.Cleanup(func() { a2aHubImageInspect, a2aHubImageExists, a2aHubImageBuild = inspect, existsFn, build })
	a2aHubImageInspect = func(context.Context, string) (docker.ImageInspectResponse, error) {
		if labels == nil {
			return docker.ImageInspectResponse{}, errors.New("no such image")
		}
		return docker.ImageInspectResponse{Config: &docker.ImageConfig{Labels: labels}}, nil
	}
	a2aHubImageExists = func(context.Context, string) (bool, error) { return exists, nil }
	a2aHubImageBuild = func(_ context.Context, req docker.BuildRequest, _ io.Writer) error {
		if _, err := os.Stat(req.Dockerfile); err != nil {
			t.Errorf("build Dockerfile %s not staged: %v", req.Dockerfile, err)
		}
		builds = append(builds, req)
		return nil
	}
	return &builds
}

func TestEnsureA2AHubImageBuildsMissingImage(t *testing.T) {
	root := writeA2AHubAppRoot(t)
	builds := stubA2AHubDocker(t, nil, false)
	image, err := EnsureA2AHubImage(context.Background(), A2AHubImageConfig{Paths: model.Paths{AppRoot: root}, Home: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureA2AHubImage: %v", err)
	}
	if image != "enclave-a2a-hub:latest" {
		t.Fatalf("image = %q", image)
	}
	if len(*builds) != 1 {
		t.Fatalf("builds = %d, want 1", len(*builds))
	}
	want, _ := a2aHubBuildHash(root)
	req := (*builds)[0]
	if req.Labels[A2AHubLabelHash] != want || !slices.Equal(req.Tags, []string{A2AHubImage}) {
		t.Fatalf("build request labels %v tags %v, want hash %s", req.Labels, req.Tags, want)
	}
}

func TestEnsureA2AHubImageSkipsCurrentImage(t *testing.T) {
	root := writeA2AHubAppRoot(t)
	hash, err := a2aHubBuildHash(root)
	if err != nil {
		t.Fatal(err)
	}
	builds := stubA2AHubDocker(t, map[string]string{A2AHubLabelHash: hash}, true)
	if _, err := EnsureA2AHubImage(context.Background(), A2AHubImageConfig{Paths: model.Paths{AppRoot: root}, Home: t.TempDir()}); err != nil {
		t.Fatalf("EnsureA2AHubImage: %v", err)
	}
	if len(*builds) != 0 {
		t.Fatalf("current image rebuilt %d times", len(*builds))
	}
	if _, err := EnsureA2AHubImage(context.Background(), A2AHubImageConfig{Paths: model.Paths{AppRoot: root}, Home: t.TempDir(), ForceRebuild: true}); err != nil {
		t.Fatalf("EnsureA2AHubImage forced: %v", err)
	}
	if len(*builds) != 1 {
		t.Fatalf("forced rebuild ran %d builds, want 1", len(*builds))
	}
}

func TestEnsureA2AHubImageNoRebuild(t *testing.T) {
	builds := stubA2AHubDocker(t, nil, false)
	cfg := A2AHubImageConfig{Paths: model.Paths{AppRoot: t.TempDir()}, Home: t.TempDir(), NoRebuild: true}
	if _, err := EnsureA2AHubImage(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "builds are disabled") {
		t.Fatalf("EnsureA2AHubImage without image = %v, want builds disabled error", err)
	}
	stubA2AHubDocker(t, nil, true)
	if image, err := EnsureA2AHubImage(context.Background(), cfg); err != nil || image != A2AHubImage {
		t.Fatalf("EnsureA2AHubImage with image = %q, %v", image, err)
	}
	if len(*builds) != 0 {
		t.Fatalf("NoRebuild built the image")
	}
}
