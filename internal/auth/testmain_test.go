// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package auth

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Prevent host XDG environment variables from overriding the home-based
	// path resolution used by tests. Without this, tests that pass t.TempDir()
	// as home share the real host XDG directories, causing cross-test pollution.
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		if err := os.Unsetenv(key); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}
