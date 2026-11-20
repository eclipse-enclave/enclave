// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build windows

package vm

import (
	"os"
	"os/exec"
)

// The vm backend needs KVM; these exist so the package compiles for the
// Windows launcher.
func fileID(os.FileInfo) string { return "" }

func ownedPrivateDir(string) bool { return false }

func ownSession(*exec.Cmd) {}
