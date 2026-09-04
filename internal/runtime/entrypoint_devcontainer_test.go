// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
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
