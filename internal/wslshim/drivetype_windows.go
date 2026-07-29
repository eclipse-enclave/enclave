// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build windows

package wslshim

import "golang.org/x/sys/windows"

func systemDriveType(root string) driveKind {
	ptr, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return driveOther
	}
	if windows.GetDriveType(ptr) == windows.DRIVE_REMOTE {
		return driveRemote
	}
	return driveOther
}
