// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package runtime

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Keep host config overrides from redirecting tests that use t.TempDir()
	// as their home into the real host's config and state directories.
	// XDG_RUNTIME_DIR and SSH_AUTH_SOCK are protected from worktree mounts, so
	// they would change which mounts tests get.
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "SSH_AUTH_SOCK", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_COUNT"} {
		if err := os.Unsetenv(key); err != nil {
			panic(err)
		}
	}
	if err := os.Setenv("GIT_CONFIG_NOSYSTEM", "1"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
