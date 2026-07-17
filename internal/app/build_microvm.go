// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"enclave/internal/backend"
	backendqemu "enclave/internal/backend/qemu"
	"enclave/internal/config"
	"enclave/internal/logx"
	"enclave/internal/model"
	"enclave/internal/util"
)

const qemuBundleBuildStampFile = "enclave-vm-build.json"

type qemuBundleBuildStamp struct {
	Hash string `json:"hash"`
	Tool string `json:"tool"`
}

func ensureQEMUBundle(input *CommandInput, opts model.Options, buildCfg buildConfig, host model.Host, profile model.Profile) (buildConfig, int) {
	if buildCfg.Devcontainer != nil {
		logx.Errorf("qemu backend does not support devcontainer builds")
		return buildConfig{}, 1
	}
	if strings.TrimSpace(opts.BaseImage) != "" {
		logx.Errorf("qemu backend does not support --base-image")
		return buildConfig{}, 1
	}
	// --image-name points at a prebuilt bundle directory: it is used read-only
	// and never becomes a build target, so a stray, relative, or Docker-tag-style
	// value can never be passed to os.RemoveAll. Rebuilds always target the
	// managed cache path below.
	if opts.ImageNameSet {
		logx.Infof("Using prebuilt qemu bundle: %s", buildCfg.ImageName)
		if opts.ForceRebuild {
			logx.Warnf("Ignoring --rebuild: --image-name selects a prebuilt qemu bundle and is never rebuilt in place; omit --image-name to rebuild into the managed cache.")
		}
		if err := ensureExistingQEMUBundle(buildCfg.ImageName); err != nil {
			logx.Errorf("%v", err)
			return buildConfig{}, 1
		}
		return buildCfg, 0
	}
	plan, err := resolveRuntimeImageBuildPlan(input.Ctx.Paths, buildCfg, opts.BuildOptions, opts.Tool, host.Home, false, time.Now().UTC())
	if err != nil {
		logx.Errorf("%v", err)
		return buildConfig{}, 1
	}
	assetHash, err := qemuBundleAssetHash(input.Ctx.Paths.AppRoot)
	if err != nil {
		logx.Errorf("%v", err)
		return buildConfig{}, 1
	}
	bundleConfig := qemuBundleConfigForProfile(profile)
	configHash, err := qemuBundleConfigHash(bundleConfig)
	if err != nil {
		logx.Errorf("%v", err)
		return buildConfig{}, 1
	}
	bundleHash := plan.CombinedHash + "-" + assetHash + "-" + configHash
	bundleDir := qemuBundleDir(host.Home, opts.Tool, bundleHash)
	buildCfg.ImageName = bundleDir
	logx.Infof("Using qemu bundle: %s", bundleDir)
	if opts.NoRebuild {
		logx.Warnf("Skipping qemu bundle build due to --no-rebuild.")
		if err := ensureExistingQEMUBundle(bundleDir); err != nil {
			logx.Errorf("%v", err)
			return buildConfig{}, 1
		}
		return buildCfg, 0
	}
	current, err := qemuBundleCurrent(bundleDir, opts.Tool, bundleHash, bundleConfig)
	if err != nil {
		logx.Debugf("qemu bundle stamp check failed: %v", err)
	}
	if opts.ForceRebuild || !current || plan.AgentUpdates.NeedsRebuild {
		if opts.ForceRebuild {
			logx.Infof("Forcing qemu bundle rebuild.")
		} else if !current {
			logx.Infof("QEMU bundle inputs changed, rebuilding automatically.")
		} else {
			logx.Infof("Agent update interval elapsed, rebuilding qemu bundle automatically.")
		}
		if err := buildQEMUBundle(context.Background(), input.Ctx.Paths, host, bundleDir, opts.Tool, bundleHash, bundleConfig); err != nil {
			logx.Errorf("%v", err)
			return buildConfig{}, 1
		}
		if err := commitAgentUpdatePlan(plan.AgentUpdates, host.Home); err != nil {
			logx.Warnf("Failed to write agent update stamp: %v", err)
		}
	}
	return buildCfg, 0
}

// qemuMicrovmCacheRoot is the managed root under which all built bundles live.
// buildQEMUBundle refuses to clear any directory outside it. It resolves via
// the shared XDG cache root so bundles sit next to the other enclave caches.
func qemuMicrovmCacheRoot(home string) string {
	return filepath.Join(config.HostCacheDir(home), "microvm")
}

func qemuBundleDir(home string, tool string, combinedHash string) string {
	return filepath.Join(qemuMicrovmCacheRoot(home), sanitizeBundlePart(tool), model.ShortHash(combinedHash))
}

func sanitizeBundlePart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "unknown"
	}
	return out
}

func ensureExistingQEMUBundle(dir string) error {
	for _, name := range []string{"vmlinuz", "initramfs.cpio"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("qemu bundle %q is missing %s; rerun without --no-rebuild or pass --rebuild", dir, name)
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("qemu bundle %q has non-file %s", dir, name)
		}
	}
	return nil
}

func qemuBundleConfigForProfile(profile model.Profile) backendqemu.BundleConfig {
	memoryMiB := backendqemu.DefaultMemoryMiB
	if profile.QEMUMinMemoryMiB > memoryMiB {
		memoryMiB = profile.QEMUMinMemoryMiB
	}
	return backendqemu.BundleConfig{MemoryMiB: memoryMiB}
}

func qemuBundleConfigHash(cfg backendqemu.BundleConfig) (string, error) {
	data, err := qemuBundleConfigJSON(cfg)
	if err != nil {
		return "", err
	}
	return util.HashString(string(data)), nil
}

func qemuBundleConfigJSON(cfg backendqemu.BundleConfig) ([]byte, error) {
	if cfg.MemoryMiB <= 0 {
		return nil, fmt.Errorf("qemu bundle memory must be positive, got %d MiB", cfg.MemoryMiB)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func writeQEMUBundleConfig(dir string, cfg backendqemu.BundleConfig) error {
	data, err := qemuBundleConfigJSON(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, backendqemu.BundleConfigFile), data, 0o600); err != nil {
		return fmt.Errorf("write qemu bundle config: %w", err)
	}
	return nil
}

func qemuBundleCurrent(dir string, tool string, hash string, cfg backendqemu.BundleConfig) (bool, error) {
	if err := ensureExistingQEMUBundle(dir); err != nil {
		return false, nil
	}
	expectedConfig, err := qemuBundleConfigJSON(cfg)
	if err != nil {
		return false, err
	}
	currentConfig, err := os.ReadFile(filepath.Join(dir, backendqemu.BundleConfigFile)) // #nosec G304 -- dir is enclave-managed cache path.
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !bytes.Equal(currentConfig, expectedConfig) {
		return false, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, qemuBundleBuildStampFile)) // #nosec G304 -- dir is enclave-managed cache path.
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var stamp qemuBundleBuildStamp
	if err := json.Unmarshal(data, &stamp); err != nil {
		return false, err
	}
	return stamp.Hash == hash && stamp.Tool == tool, nil
}

func buildQEMUBundle(ctx context.Context, paths model.Paths, host model.Host, bundleDir string, tool string, hash string, cfg backendqemu.BundleConfig) error {
	selection := runtimeImageSelection{Tools: []string{tool}}
	contextDir, cleanup, err := prepareBuildContext(paths, selection)
	if err != nil {
		return err
	}
	defer cleanup()
	script := filepath.Join(contextDir, "runtime-assets", "microvm", "alpine", "build-bundle.sh")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("qemu bundle builder %s: %w", script, err)
	}
	// Defense in depth: bundleDir is always the managed cache path, but never
	// os.RemoveAll a directory outside it (guards against any future caller).
	if !util.PathWithin(qemuMicrovmCacheRoot(host.Home), bundleDir) {
		return fmt.Errorf("refusing to clear qemu bundle dir outside the managed cache: %s", bundleDir)
	}
	if err := os.RemoveAll(bundleDir); err != nil {
		return fmt.Errorf("clear qemu bundle dir: %w", err)
	}
	if err := os.MkdirAll(bundleDir, 0o700); err != nil {
		return fmt.Errorf("create qemu bundle dir: %w", err)
	}
	logx.Infof("Building qemu microvm bundle (%s). This may take a few minutes on first run.", tool)
	cmd := exec.CommandContext(ctx, script, bundleDir, contextDir, tool, host.UID, host.GID) // #nosec G204 -- script path and args are generated from trusted build config.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("qemu bundle build failed: %w", err)
	}
	if err := ensureExistingQEMUBundle(bundleDir); err != nil {
		return err
	}
	if err := writeQEMUBundleConfig(bundleDir, cfg); err != nil {
		return err
	}
	stamp := qemuBundleBuildStamp{Hash: hash, Tool: tool}
	data, err := json.MarshalIndent(stamp, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(bundleDir, qemuBundleBuildStampFile), data, 0o600); err != nil {
		return fmt.Errorf("write qemu bundle stamp: %w", err)
	}
	return nil
}

func isQEMUBackend(opts model.Options) bool {
	return strings.TrimSpace(opts.Backend) == backend.NameQEMU
}
