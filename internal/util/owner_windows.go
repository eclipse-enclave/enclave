// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build windows

package util

import "os"

// FileInfoOwnedBy always reports true on Windows, where POSIX ownership does
// not apply.
func FileInfoOwnedBy(os.FileInfo, string) bool {
	return true
}
