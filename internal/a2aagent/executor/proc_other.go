// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !linux

package executor

import (
	"errors"
	"os/exec"
)

// The adapter runs only inside Linux session containers.
var errUnsupported = errors.New("executor: process-group execution requires linux")

func setProcessGroup(*exec.Cmd) {}

func platformProcessOps() (processOps, error) { return processOps{}, errUnsupported }
