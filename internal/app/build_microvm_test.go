// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	backendqemu "enclave/internal/backend/qemu"
	"enclave/internal/model"
)

// R3: editing a microVM bundle asset must change the bundle freshness hash, or
// stamped bundles stay marked current and never rebuild.
func TestQEMUBundleAssetHashChangesWithAssets(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "runtime-assets", "microvm", "alpine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(dir, "build-bundle.sh")

	if err := os.WriteFile(asset, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	h1, err := qemuBundleAssetHash(root)
	if err != nil {
		t.Fatalf("hash v1: %v", err)
	}
	if err := os.WriteFile(asset, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	h2, err := qemuBundleAssetHash(root)
	if err != nil {
		t.Fatalf("hash v2: %v", err)
	}
	if h1 == "" || h2 == "" {
		t.Fatalf("empty asset hash: h1=%q h2=%q", h1, h2)
	}
	if h1 == h2 {
		t.Fatalf("asset hash unchanged after editing build-bundle.sh; bundle would not rebuild")
	}
}

func TestQEMUBundleConfigForProfileUsesToolOverride(t *testing.T) {
	cfg := qemuBundleConfigForProfile(model.Profile{Name: "codex", QEMUMinMemoryMiB: 4096})
	if cfg.MemoryMiB != 4096 {
		t.Fatalf("MemoryMiB = %d, want 4096", cfg.MemoryMiB)
	}
}

func TestQEMUBundleConfigForProfileUsesDefault(t *testing.T) {
	cfg := qemuBundleConfigForProfile(model.Profile{Name: "claude"})
	if cfg.MemoryMiB != backendqemu.DefaultMemoryMiB {
		t.Fatalf("MemoryMiB = %d, want %d", cfg.MemoryMiB, backendqemu.DefaultMemoryMiB)
	}
}

func TestQEMUBundleConfigForProfileDoesNotLowerDefault(t *testing.T) {
	cfg := qemuBundleConfigForProfile(model.Profile{Name: "tiny", QEMUMinMemoryMiB: 1024})
	if cfg.MemoryMiB != backendqemu.DefaultMemoryMiB {
		t.Fatalf("MemoryMiB = %d, want default %d", cfg.MemoryMiB, backendqemu.DefaultMemoryMiB)
	}
}

func TestQEMUBundleConfigHashChangesWithMemory(t *testing.T) {
	h1, err := qemuBundleConfigHash(backendqemu.BundleConfig{MemoryMiB: backendqemu.DefaultMemoryMiB})
	if err != nil {
		t.Fatalf("hash default memory: %v", err)
	}
	h2, err := qemuBundleConfigHash(backendqemu.BundleConfig{MemoryMiB: backendqemu.DefaultMemoryMiB + 2048})
	if err != nil {
		t.Fatalf("hash changed memory: %v", err)
	}
	if h1 == h2 {
		t.Fatalf("bundle config hash unchanged after memory change")
	}
}

func TestWriteQEMUBundleConfig(t *testing.T) {
	dir := t.TempDir()
	if err := writeQEMUBundleConfig(dir, backendqemu.BundleConfig{MemoryMiB: backendqemu.DefaultMemoryMiB}); err != nil {
		t.Fatalf("writeQEMUBundleConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, backendqemu.BundleConfigFile))
	if err != nil {
		t.Fatalf("read bundle config: %v", err)
	}
	var cfg backendqemu.BundleConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse bundle config: %v", err)
	}
	if cfg.MemoryMiB != backendqemu.DefaultMemoryMiB {
		t.Fatalf("memoryMiB = %d, want %d", cfg.MemoryMiB, backendqemu.DefaultMemoryMiB)
	}
}

func TestQEMUBundleCurrentRequiresExpectedConfig(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"vmlinuz", "initramfs.cpio"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stamp := []byte(`{"hash":"h","tool":"codex"}`)
	if err := os.WriteFile(filepath.Join(dir, qemuBundleBuildStampFile), stamp, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := backendqemu.BundleConfig{MemoryMiB: backendqemu.DefaultMemoryMiB}
	current, err := qemuBundleCurrent(dir, "codex", "h", cfg)
	if err != nil {
		t.Fatalf("qemuBundleCurrent without config: %v", err)
	}
	if current {
		t.Fatal("bundle without expected config should not be current")
	}
	if err := writeQEMUBundleConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	current, err = qemuBundleCurrent(dir, "codex", "h", cfg)
	if err != nil {
		t.Fatalf("qemuBundleCurrent with config: %v", err)
	}
	if !current {
		t.Fatal("bundle with matching stamp and config should be current")
	}
}

// R2: --image-name is a prebuilt bundle used read-only; it must never be a
// build/RemoveAll target, even with --rebuild.
func TestEnsureQEMUBundleImageNameIsReadOnly(t *testing.T) {
	bundleDir := t.TempDir()
	for _, name := range []string{"vmlinuz", "initramfs.cpio"} {
		if err := os.WriteFile(filepath.Join(bundleDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := filepath.Join(bundleDir, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts := model.Options{}
	opts.ImageNameSet = true
	opts.ForceRebuild = true // even asking for a rebuild must not delete the dir
	buildCfg := buildConfig{ImageName: bundleDir}

	_, code := ensureQEMUBundle(&CommandInput{}, opts, buildCfg, model.Host{Home: t.TempDir()}, model.Profile{Name: "tool"})
	if code != 0 {
		t.Fatalf("expected prebuilt bundle to be accepted, got exit code %d", code)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("prebuilt bundle was cleared/modified (R2 regression): %v", err)
	}
}
