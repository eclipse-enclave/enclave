// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !windows

package util

import (
	"os"
	"strconv"
	"syscall"
)

// PathOwnedBy reports whether path is owned by the numeric uid. It returns
// true when ownership cannot be determined (unparseable uid, stat failure, or
// a platform without POSIX ownership) so callers only act on a definite
// mismatch.
func PathOwnedBy(path string, uid string) bool {
	want, err := strconv.ParseUint(uid, 10, 32)
	if err != nil {
		return true
	}
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	return uint64(stat.Uid) == want
}
