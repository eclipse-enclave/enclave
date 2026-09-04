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
)

const pnpmTestStoreDir = "/home/agent/.local/share/pnpm/store"

func pnpmGlobalConfigPath(home string) string {
	return filepath.Join(home, ".config", "pnpm", "rc")
}

// pnpm 9 and 10 read the store location from pnpm's global config file; pnpm 11
// and later read PNPM_CONFIG_STORE_DIR from the environment and ignore the file.
func TestEntrypointWritesPnpmStoreToGlobalConfig(t *testing.T) {
	home, output, err := runEntrypointCommand(t, []string{
		"PNPM_CONFIG_STORE_DIR=" + pnpmTestStoreDir,
	}, "true")
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	assertFileContent(t, pnpmGlobalConfigPath(home), "store-dir="+pnpmTestStoreDir+"\n")
}

func TestEntrypointWritesPnpmStoreToXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	xdgDir := filepath.Join(home, "xdg")

	output, err := runEntrypointCommandInHome(t, home, []string{
		"PNPM_CONFIG_STORE_DIR=" + pnpmTestStoreDir,
		"XDG_CONFIG_HOME=" + xdgDir,
	}, "true")
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	assertFileContent(t, filepath.Join(xdgDir, "pnpm", "rc"), "store-dir="+pnpmTestStoreDir+"\n")
}

// store-dir in ~/.npmrc makes npm 11+ warn about an unknown user config on every
// invocation, so it must not land there.
func TestEntrypointLeavesNpmrcAlone(t *testing.T) {
	home := t.TempDir()
	npmrc := filepath.Join(home, ".npmrc")
	writeFile(t, npmrc, "registry=https://example.test/\n")

	output, err := runEntrypointCommandInHome(t, home, []string{
		"PNPM_CONFIG_STORE_DIR=" + pnpmTestStoreDir,
	}, "true")
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	assertFileContent(t, npmrc, "registry=https://example.test/\n")
}

// ~/.npmrc persists across sessions, so an entry written by an earlier release
// has to be cleaned up rather than left to keep warning.
func TestEntrypointRemovesStoreDirFromNpmrc(t *testing.T) {
	home := t.TempDir()
	npmrc := filepath.Join(home, ".npmrc")
	writeFile(t, npmrc, "registry=https://example.test/\nstore-dir=/stale/store\n")

	output, err := runEntrypointCommandInHome(t, home, []string{
		"PNPM_CONFIG_STORE_DIR=" + pnpmTestStoreDir,
	}, "true")
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	assertFileContent(t, npmrc, "registry=https://example.test/\n")
	assertFileContent(t, pnpmGlobalConfigPath(home), "store-dir="+pnpmTestStoreDir+"\n")
}

func TestEntrypointKeepsOtherPnpmGlobalConfigEntries(t *testing.T) {
	home := t.TempDir()
	rc := pnpmGlobalConfigPath(home)
	writeFile(t, rc, "store-dir=/stale/store\nnode-linker=hoisted\n")

	output, err := runEntrypointCommandInHome(t, home, []string{
		"PNPM_CONFIG_STORE_DIR=" + pnpmTestStoreDir,
	}, "true")
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	assertFileContent(t, rc, "node-linker=hoisted\nstore-dir="+pnpmTestStoreDir+"\n")
}

func TestEntrypointRewritesPnpmStoreIdempotently(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 2; i++ {
		output, err := runEntrypointCommandInHome(t, home, []string{
			"PNPM_CONFIG_STORE_DIR=" + pnpmTestStoreDir,
		}, "true")
		if err != nil {
			t.Fatalf("entrypoint run %d failed: %v\noutput:\n%s", i, err, output)
		}
	}

	data, err := os.ReadFile(pnpmGlobalConfigPath(home))
	if err != nil {
		t.Fatalf("read pnpm config: %v", err)
	}
	if got := strings.Count(string(data), "store-dir="); got != 1 {
		t.Fatalf("store-dir entries = %d, want 1\nconfig:\n%s", got, data)
	}
}

func TestEntrypointDoesNotExportLegacyStoreEnv(t *testing.T) {
	home, output, err := runEntrypointCommand(t, []string{
		"PNPM_CONFIG_STORE_DIR=" + pnpmTestStoreDir,
	}, "bash", "-c", `printf 'store=%s\n' "${npm_config_store_dir:-}" > "$HOME/pnpm-env.out"`)
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	values := readEntrypointEnvFile(t, filepath.Join(home, "pnpm-env.out"))
	if values["store"] != "" {
		t.Fatalf("npm_config_store_dir leaked into the environment: %q", values["store"])
	}
}

func TestEntrypointSkipsPnpmConfigWithoutStoreDir(t *testing.T) {
	home, output, err := runEntrypointCommand(t, nil, "true")
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	if _, err := os.Stat(pnpmGlobalConfigPath(home)); !os.IsNotExist(err) {
		t.Fatalf("expected no pnpm global config, stat error = %v", err)
	}
}
