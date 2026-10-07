// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build windows

package config

// accessAllowsCreate always reports true on Windows, where enclave only runs
// as the WSL launcher and never resolves host paths.
func accessAllowsCreate(string) bool {
	return true
}
