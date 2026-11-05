// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !linux

package a2ahub

import (
	"syscall"
	"time"
)

// The hub only runs on Linux; elsewhere the option is skipped so the package builds.
func setUserTimeout(syscall.RawConn, time.Duration) error { return nil }
