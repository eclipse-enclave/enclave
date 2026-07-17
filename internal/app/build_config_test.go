// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enclave/internal/model"
)

func TestBuildArgsHashSuffixChanges(t *testing.T) {
	base := model.BuildOptions{
		ImageName: "enclave:latest",
	}

	baseSuffix := buildArgsHashSuffix(base, "claude")

	withFeatures := base
	withFeatures.Features = []string{"devtools"}
	featuresSuffix := buildArgsHashSuffix(withFeatures, "claude")
	if baseSuffix == featuresSuffix {
		t.Fatalf("expected features change to alter hash suffix")
	}
}

func TestBuildArgsHashSuffixOrderIndependent(t *testing.T) {
	base := model.BuildOptions{
		ImageName: "enclave:latest",
	}

	featuresA := base
	featuresA.Features = []string{"devtools", "python-dev"}
	featuresB := base
	featuresB.Features = []string{"python-dev", "devtools"}
	if buildArgsHashSuffix(featuresA, "codex") != buildArgsHashSuffix(featuresB, "codex") {
		t.Fatalf("expected features ordering to produce the same hash suffix")
	}
}

func TestResolveBuildConfigCustomImageNameFeaturesOrderIndependent(t *testing.T) {
	optsA := model.BuildOptions{
		ImageName: "enclave:latest",
		Features:  []string{"devtools", "python-dev"},
	}
	optsB := optsA
	optsB.Features = []string{"python-dev", "devtools"}

	cfgA, err := resolveBuildConfig(optsA, "codex", model.Project{}, "")
	if err != nil {
		t.Fatalf("resolve build config: %v", err)
	}
	cfgB, err := resolveBuildConfig(optsB, "codex", model.Project{}, "")
	if err != nil {
		t.Fatalf("resolve build config: %v", err)
	}
	if cfgA.ImageName != cfgB.ImageName {
		t.Fatalf("expected features ordering to produce the same image name, got %q and %q", cfgA.ImageName, cfgB.ImageName)
	}
	if cfgA.ImageName == model.AppName+":latest" {
		t.Fatalf("expected a custom image name for feature overrides")
	}
	if !strings.Contains(cfgA.ImageName, "custom-") {
		t.Fatalf("expected custom image name prefix, got %q", cfgA.ImageName)
	}
}

func TestResolveBuildConfigDefaultsToPerToolImageName(t *testing.T) {
	cfg, err := resolveBuildConfig(model.BuildOptions{ImageName: "enclave:latest"}, "claude", model.Project{}, "")
	if err != nil {
		t.Fatalf("resolve build config: %v", err)
	}
	want := model.AppName + "-claude:latest"
	if cfg.ImageName != want {
		t.Fatalf("expected per-tool default image name %q, got %q", want, cfg.ImageName)
	}
}

func TestResolveFeaturesArgDevcontainerDefault(t *testing.T) {
	opts := model.BuildOptions{Devcontainer: true}
	if got := resolveFeaturesArg(opts); got != "" {
		t.Fatalf("expected empty features for devcontainer mode, got %q", got)
	}
}

func TestResolveFeaturesArgDevcontainerExplicitFeatures(t *testing.T) {
	opts := model.BuildOptions{
		Devcontainer: true,
		Features:     []string{"devtools"},
	}
	if got := resolveFeaturesArg(opts); got != "devtools" {
		t.Fatalf("expected explicit features to override devcontainer default, got %q", got)
	}
}

func TestResolveFeaturesArgExplicitEmpty(t *testing.T) {
	opts := model.BuildOptions{
		Features: []string{},
	}
	if got := resolveFeaturesArg(opts); got != "" {
		t.Fatalf("expected empty features arg for explicit empty selection, got %q", got)
	}
}

func TestDevcontainerFeatureSelectionMessageEmpty(t *testing.T) {
	if got := devcontainerFeatureSelectionMessage(""); got != "Devcontainer mode: no enclave features enabled (use --features to add)." {
		t.Fatalf("unexpected message: %q", got)
	}
}

func TestDevcontainerFeatureSelectionMessageList(t *testing.T) {
	if got := devcontainerFeatureSelectionMessage("node-dev python-dev"); got != "Devcontainer mode: features enabled: node-dev, python-dev" {
		t.Fatalf("unexpected message: %q", got)
	}
}

func TestResolveBuildConfigCustomImageNamePerTool(t *testing.T) {
	opts := model.BuildOptions{
		ImageName: "enclave:latest",
		Features:  []string{"devtools"},
	}

	cfg, err := resolveBuildConfig(opts, "claude", model.Project{}, "")
	if err != nil {
		t.Fatalf("resolve build config: %v", err)
	}
	prefix := model.AppName + "-claude:custom-"
	if !strings.HasPrefix(cfg.ImageName, prefix) {
		t.Fatalf("expected per-tool custom image name prefix %q, got %q", prefix, cfg.ImageName)
	}
}

func TestResolveBuildConfigDevcontainerRejectsNonDebianImage(t *testing.T) {
	projectRoot := t.TempDir()
	writeDevcontainerImageConfig(t, projectRoot, "node:22-alpine")

	opts := model.BuildOptions{
		Devcontainer: true,
		ImageName:    model.ImageName,
	}
	_, err := resolveBuildConfig(opts, "claude", model.Project{RealDir: projectRoot, Dir: "/workspace/project"}, "")
	if err == nil {
		t.Fatalf("expected resolveBuildConfig to fail")
	}
	if !strings.Contains(err.Error(), "appears non-Debian") {
		t.Fatalf("expected non-Debian error, got %q", err.Error())
	}
}

func TestResolveBuildConfigDevcontainerAllowsNonDebianWithForce(t *testing.T) {
	projectRoot := t.TempDir()
	writeDevcontainerImageConfig(t, projectRoot, "node:22-alpine")

	opts := model.BuildOptions{
		Devcontainer:   true,
		ForceBaseImage: true,
		ImageName:      model.ImageName,
	}
	cfg, err := resolveBuildConfig(opts, "claude", model.Project{RealDir: projectRoot, Dir: "/workspace/project"}, "")
	if err != nil {
		t.Fatalf("resolveBuildConfig returned error: %v", err)
	}
	if cfg.BaseImage != "node:22-alpine" {
		t.Fatalf("unexpected base image: %q", cfg.BaseImage)
	}
}

func writeDevcontainerImageConfig(t *testing.T, root string, image string) {
	t.Helper()
	configPath := filepath.Join(root, model.DevcontainerDir, model.DevcontainerFilename)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(configPath), err)
	}
	content := `{"image":"` + image + `"}`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", configPath, err)
	}
}
