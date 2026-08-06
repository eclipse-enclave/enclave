// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build windows

package app

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func availableBytesAtPath(path string) (uint64, error) {
	resolved, err := existingPathForStatfs(path)
	if err != nil {
		return 0, err
	}
	name, err := windows.UTF16PtrFromString(resolved)
	if err != nil {
		return 0, fmt.Errorf("encode filesystem path %s: %w", resolved, err)
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(name, &available, nil, nil); err != nil {
		return 0, fmt.Errorf("stat filesystem for %s: %w", resolved, err)
	}
	return available, nil
}
