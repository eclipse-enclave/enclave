// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build enclave_no_embed

// Package assets omits the embedded runtime tree from package-managed builds.
package assets

import "io/fs"

// FS returns no embedded assets in package-managed builds.
func FS() fs.FS {
	return nil
}
