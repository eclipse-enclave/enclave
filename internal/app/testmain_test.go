// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"testing"

	"enclave/internal/model"
)

func TestMain(m *testing.M) {
	// Prevent host XDG environment variables from overriding the home-based
	// path resolution used by tests. Without this, tests that pass t.TempDir()
	// as home share the real host XDG directories, causing cross-test pollution.
	// XDG_RUNTIME_DIR and SSH_AUTH_SOCK are protected from mounts, so they
	// would change which paths the mount guard refuses.
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "SSH_AUTH_SOCK"} {
		if err := os.Unsetenv(key); err != nil {
			panic(err)
		}
	}
	// Opt-ins in the developer's shell would otherwise flip root-guard and
	// mount-guard results.
	for _, key := range []string{model.EnvAllowRoot, model.EnvAllowSensitiveMounts} {
		if err := os.Unsetenv(key); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}
