// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Tests resolve host paths from a temporary home. Inherited XDG overrides
	// would send them to the real host directories instead.
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR"} {
		if err := os.Unsetenv(key); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}
