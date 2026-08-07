// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build windows

package wslshim

import (
	"os/exec"
	"syscall"
)

// setCommandLine hands CreateProcess the exact command line the launcher built,
// bypassing the quoting os/exec would otherwise apply to cmd.Args.
func setCommandLine(cmd *exec.Cmd, cmdLine string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: cmdLine}
}
