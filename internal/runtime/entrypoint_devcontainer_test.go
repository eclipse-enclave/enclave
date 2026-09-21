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
	"testing"
)

func TestEntrypointNormalizesPnpmStoreForDevcontainerUser(t *testing.T) {
	captureEnv := `printf 'PNPM_CONFIG_STORE_DIR=%s\n' "${PNPM_CONFIG_STORE_DIR:-}" > "$HOME/pnpm-store-dir.out"`
	home, output, err := runEntrypointCommand(t, []string{
		"ENCLAVE_DEVCONTAINER=1",
		"ENCLAVE_DEVCONTAINER_REMOTE_USER=node",
		"PNPM_CONFIG_STORE_DIR=/home/node/.local/share/pnpm/store",
	}, "bash", "-c", captureEnv)
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	values := readEntrypointEnvFile(t, filepath.Join(home, "pnpm-store-dir.out"))
	want := filepath.Join(home, ".local", "share", "pnpm", "store")
	if values["PNPM_CONFIG_STORE_DIR"] != want {
		t.Fatalf("PNPM_CONFIG_STORE_DIR = %q, want %q", values["PNPM_CONFIG_STORE_DIR"], want)
	}
}

func TestEntrypointNormalizesXDGStateHomeForDevcontainerUser(t *testing.T) {
	toolsDir := t.TempDir()
	setupDir := filepath.Join(toolsDir, "pi", "entrypoint.d")
	if err := os.MkdirAll(setupDir, 0o755); err != nil {
		t.Fatalf("mkdir tool setup dir: %v", err)
	}
	setupScript := filepath.Join(setupDir, "setup.sh")
	if err := os.WriteFile(setupScript, []byte(`printf 'XDG_STATE_HOME=%s\n' "${XDG_STATE_HOME:-}" > "$HOME/xdg-state-home.out"
`), 0o755); err != nil {
		t.Fatalf("write tool setup script: %v", err)
	}

	home, output, err := runEntrypointCommand(t, []string{
		"ENCLAVE_DEVCONTAINER=1",
		"ENCLAVE_DEVCONTAINER_REMOTE_USER=node",
		"XDG_STATE_HOME=/home/node/.local/state",
		"ENCLAVE_TOOLS_DIR=" + toolsDir,
	}, "true")
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	values := readEntrypointEnvFile(t, filepath.Join(home, "xdg-state-home.out"))
	want := filepath.Join(home, ".local", "state")
	if values["XDG_STATE_HOME"] != want {
		t.Fatalf("XDG_STATE_HOME = %q, want %q", values["XDG_STATE_HOME"], want)
	}
}
