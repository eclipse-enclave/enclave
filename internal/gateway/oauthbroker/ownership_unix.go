// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package oauthbroker

import (
	"os"
	"syscall"
)

func inheritFileOwner(file *os.File, dir string) error {
	info, err := os.Stat(dir) // #nosec G703 -- validated private directory containing the new credential file.
	if err != nil {
		return ErrState
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ErrState
	}
	current, err := file.Stat()
	if err != nil {
		return ErrState
	}
	existing, ok := current.Sys().(*syscall.Stat_t)
	if !ok {
		return ErrState
	}
	if existing.Uid == owner.Uid && existing.Gid == owner.Gid {
		return nil
	}
	if err := file.Chown(int(owner.Uid), int(owner.Gid)); err != nil {
		return ErrState
	}
	return nil
}
