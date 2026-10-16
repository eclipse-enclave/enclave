// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build windows

package hosthooks

import "os/exec"

func configureProcess(*exec.Cmd) {}
func killProcessGroup(*exec.Cmd) {}
