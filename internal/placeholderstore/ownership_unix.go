//go:build !windows

// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package placeholderstore

import (
	"fmt"
	"os"
	"syscall"
)

func preserveOwner(file *os.File, before os.FileInfo) error {
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("native owner is unavailable")
	}
	current, err := file.Stat()
	if err != nil {
		return err
	}
	created, ok := current.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("native temporary owner is unavailable")
	}
	if created.Uid == owner.Uid && created.Gid == owner.Gid {
		return nil
	}
	return file.Chown(int(owner.Uid), int(owner.Gid))
}
