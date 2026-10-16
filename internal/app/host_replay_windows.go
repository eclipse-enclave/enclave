// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build windows

package app

import "fmt"

var hostReexec = func(string, []string, []string) error {
	return fmt.Errorf("native Windows replay is unsupported; use the WSL launcher")
}
