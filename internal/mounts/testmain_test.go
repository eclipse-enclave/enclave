// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package mounts

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// The sensitive-path rule protects the SSH agent socket, so an agent in
	// the developer's or CI's environment would change which paths tests can
	// mount.
	if err := os.Unsetenv("SSH_AUTH_SOCK"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
