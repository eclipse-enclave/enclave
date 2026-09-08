// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build linux || darwin

package config

import (
	"fmt"
	"os"
	"syscall"
)

func validateProjectTagsFileOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine file owner")
	}
	if int64(stat.Uid) != int64(os.Geteuid()) {
		return fmt.Errorf("must be owned by uid %d", os.Geteuid())
	}
	return nil
}
