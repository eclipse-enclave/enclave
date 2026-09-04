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

func TestEntrypointPassesStoreOptionOnlyToPnpm(t *testing.T) {
	fakeBin := t.TempDir()
	pnpmPath := filepath.Join(fakeBin, "pnpm")
	pnpmScript := `#!/bin/sh
{
printf 'store=%s\n' "${npm_config_store_dir:-}"
printf 'args=%s\n' "$*"
} > "$HOME/pnpm-compat.out"
`
	if err := os.WriteFile(pnpmPath, []byte(pnpmScript), 0o755); err != nil {
		t.Fatalf("write pnpm shim: %v", err)
	}

	storeDir := "/home/agent/.local/share/pnpm/store"
	home, output, err := runEntrypointCommand(t, []string{
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PNPM_CONFIG_STORE_DIR=" + storeDir,
	}, "bash", "-c", `pnpm install && printf 'store=%s\n' "${npm_config_store_dir:-}" > "$HOME/pnpm-parent-env.out"`)
	if err != nil {
		t.Fatalf("entrypoint failed: %v\noutput:\n%s", err, output)
	}

	values := readEntrypointEnvFile(t, filepath.Join(home, "pnpm-compat.out"))
	if values["store"] != "" {
		t.Fatalf("npm_config_store_dir leaked into pnpm environment: %q", values["store"])
	}
	wantArgs := "--store-dir=" + storeDir + " install"
	if values["args"] != wantArgs {
		t.Fatalf("pnpm args = %q, want %q", values["args"], wantArgs)
	}
	parentValues := readEntrypointEnvFile(t, filepath.Join(home, "pnpm-parent-env.out"))
	if parentValues["store"] != "" {
		t.Fatalf("npm_config_store_dir leaked into parent environment: %q", parentValues["store"])
	}
}
