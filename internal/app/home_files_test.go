// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// commonShScript returns the absolute path to the build-script helper library.
func commonShScript(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "runtime-assets", "build-scripts", "lib", "common.sh"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return p
}

// writeHomeFilesFixture writes content to path (relative parents created) with mode.
func writeHomeFilesFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// WriteFile honors umask; force the exact mode.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

// runCopyHomeFiles sources common.sh and calls enclave_copy_home_files "$EXT"
// with HOME pointed at home. Any extraEnv entries ("KEY=value") are appended to
// the command environment (e.g. ENCLAVE_HOME_FILES_SRC).
func runCopyHomeFiles(t *testing.T, home, ext string, extraEnv ...string) (string, error) {
	t.Helper()
	script := `set -euo pipefail; . "$COMMON"; enclave_copy_home_files "$EXT"`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"COMMON=" + commonShScript(t),
		"HOME=" + home,
		"EXT=" + ext,
	}, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCopyHomeFilesMaterializesTreeAndOverwrites(t *testing.T) {
	ext := t.TempDir()
	home := t.TempDir()

	// A nested regular file, a dotfile, and an executable.
	writeHomeFilesFixture(t, filepath.Join(ext, "files", "home", ".config", "app", "conf.toml"), "kit=1\n", 0o644)
	writeHomeFilesFixture(t, filepath.Join(ext, "files", "home", ".dotfile"), "dot\n", 0o644)
	writeHomeFilesFixture(t, filepath.Join(ext, "files", "home", "bin", "run.sh"), "#!/bin/sh\n", 0o755)

	// Pre-existing target must be overwritten (kit wins).
	writeHomeFilesFixture(t, filepath.Join(home, ".config", "app", "conf.toml"), "OLD\n", 0o644)

	if out, err := runCopyHomeFiles(t, home, ext); err != nil {
		t.Fatalf("copy home files: %v\n%s", err, out)
	}

	conf, err := os.ReadFile(filepath.Join(home, ".config", "app", "conf.toml"))
	if err != nil {
		t.Fatalf("read conf: %v", err)
	}
	if string(conf) != "kit=1\n" {
		t.Fatalf("conf = %q, want %q (kit must overwrite)", string(conf), "kit=1\n")
	}

	dot, err := os.ReadFile(filepath.Join(home, ".dotfile"))
	if err != nil {
		t.Fatalf("read dotfile: %v", err)
	}
	if string(dot) != "dot\n" {
		t.Fatalf("dotfile = %q, want %q", string(dot), "dot\n")
	}

	info, err := os.Stat(filepath.Join(home, "bin", "run.sh"))
	if err != nil {
		t.Fatalf("stat run.sh: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Fatalf("run.sh perm = %o, want 0755 (mode must be preserved)", perm)
	}
}

// TestCopyHomeFilesPristineSourcePreservesMode mirrors the real build: the
// working extension tree is chmod'd a+rX (widening a 0640 file to 0644), while a
// separate pristine, agent-owned tree keeps the declared 0640. When
// ENCLAVE_HOME_FILES_SRC points at the pristine tree, the bake must read it and
// land 0640; without the env var it reads the widened working copy and lands 0644.
func TestCopyHomeFilesPristineSourcePreservesMode(t *testing.T) {
	// Working ext tree at .../features/<name>, mode already widened to 0644.
	root := t.TempDir()
	ext := filepath.Join(root, "features", "restrictive-feat")
	writeHomeFilesFixture(t, filepath.Join(ext, "files", "home", ".restrictive"), "secret\n", 0o644)

	// Pristine mirror keeps the declared 0640.
	pristine := t.TempDir()
	writeHomeFilesFixture(t, filepath.Join(pristine, "features", "restrictive-feat", "files", "home", ".restrictive"), "secret\n", 0o640)

	// With the pristine source: mode preserved at 0640.
	homePristine := t.TempDir()
	if out, err := runCopyHomeFiles(t, homePristine, ext, "ENCLAVE_HOME_FILES_SRC="+pristine); err != nil {
		t.Fatalf("copy home files (pristine): %v\n%s", err, out)
	}
	info, err := os.Stat(filepath.Join(homePristine, ".restrictive"))
	if err != nil {
		t.Fatalf("stat pristine .restrictive: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("pristine .restrictive perm = %o, want 0640 (pristine source mode must be preserved)", perm)
	}

	// Without the env var: falls back to the widened working copy (0644).
	homeWorking := t.TempDir()
	if out, err := runCopyHomeFiles(t, homeWorking, ext); err != nil {
		t.Fatalf("copy home files (working): %v\n%s", err, out)
	}
	info, err = os.Stat(filepath.Join(homeWorking, ".restrictive"))
	if err != nil {
		t.Fatalf("stat working .restrictive: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("working .restrictive perm = %o, want 0644 (fallback reads widened working copy)", perm)
	}
}

// hasMikefarahYq reports whether the yq on PATH is mikefarah/yq v4, the flavor
// the build-script selection helpers target. The kislyuk yq (jq wrapper) is
// skipped, so this test only runs where the real build toolchain is present.
func hasMikefarahYq(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("yq"); err != nil {
		return false
	}
	out, err := exec.Command("yq", "--version").CombinedOutput()
	if err != nil {
		return false
	}
	v := strings.ToLower(string(out))
	return strings.Contains(v, "mikefarah") || strings.Contains(v, "version v4")
}

func installHomeFilesScript(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "runtime-assets", "build-scripts", "install-extension-home-files.sh"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return p
}

// TestInstallExtensionHomeFilesRespectsSelection verifies the full build script
// materializes files/home for enabled features and included tools while skipping
// a defaultEnabled:false feature. Requires mikefarah/yq v4 (skipped otherwise);
// a docker-build spot-check covers selection on hosts without it.
func TestInstallExtensionHomeFilesRespectsSelection(t *testing.T) {
	if !hasMikefarahYq(t) {
		t.Skip("requires mikefarah/yq v4; host has kislyuk yq")
	}
	root := t.TempDir()
	home := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	toolsDir := filepath.Join(root, "tools")

	// Enabled feature (defaultEnabled omitted => enabled).
	writeHomeFilesFixture(t, filepath.Join(featuresDir, "enabled-feat", "spec.yaml"),
		"schemaVersion: \"1\"\nkind: mixin\nname: enabled-feat\n", 0o644)
	writeHomeFilesFixture(t, filepath.Join(featuresDir, "enabled-feat", "files", "home", "enabled.txt"), "e\n", 0o644)

	// Disabled feature (defaultEnabled: false) — its files/home must NOT be copied.
	writeHomeFilesFixture(t, filepath.Join(featuresDir, "disabled-feat", "spec.yaml"),
		"schemaVersion: \"1\"\nkind: mixin\nname: disabled-feat\ndefaultEnabled: false\n", 0o644)
	writeHomeFilesFixture(t, filepath.Join(featuresDir, "disabled-feat", "files", "home", "disabled.txt"), "d\n", 0o644)

	// Included tool (defaultIncluded omitted => included).
	writeHomeFilesFixture(t, filepath.Join(toolsDir, "included-tool", "spec.yaml"),
		"schemaVersion: \"1\"\nkind: sandbox\nname: included-tool\n", 0o644)
	writeHomeFilesFixture(t, filepath.Join(toolsDir, "included-tool", "files", "home", "tool.txt"), "t\n", 0o644)

	cmd := exec.Command("bash", installHomeFilesScript(t))
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"FEATURES=default",
		"AGENT_TOOLS=all",
		"ENCLAVE_FEATURES_DIR=" + featuresDir,
		"ENCLAVE_TOOLS_DIR=" + toolsDir,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install-extension-home-files: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(home, "enabled.txt")); err != nil {
		t.Fatalf("enabled feature file should be baked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "tool.txt")); err != nil {
		t.Fatalf("included tool file should be baked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "disabled.txt")); err == nil {
		t.Fatalf("disabled feature file must NOT be baked")
	}
}

func TestCopyHomeFilesNoDirNoop(t *testing.T) {
	ext := t.TempDir()
	home := t.TempDir()

	if out, err := runCopyHomeFiles(t, home, ext); err != nil {
		t.Fatalf("copy home files with no files/home should be a clean no-op: %v\n%s", err, out)
	}

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read home: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("home should be untouched, got %d entries", len(entries))
	}
}
