// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package config

import "golang.org/x/sys/unix"

// accessAllowsCreate asks the kernel whether the process may create entries in
// dir. Creating needs search permission as well as write permission.
func accessAllowsCreate(dir string) bool {
	return unix.Access(dir, unix.W_OK|unix.X_OK) == nil
}
