// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package wslshim

import "os/exec"

// Only Windows lets a caller supply the raw command line. The launcher never
// spawns anything on other platforms; this stub keeps the package buildable and
// testable there.
func setCommandLine(*exec.Cmd, string) {}
