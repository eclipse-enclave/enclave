// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build windows

package wslshim

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WNetGetConnectionW is not wrapped by golang.org/x/sys/windows, so it is bound
// here. NewLazySystemDLL resolves out of the system directory, which
// NewLazyDLL would not.
var (
	modMpr                 = windows.NewLazySystemDLL("mpr.dll")
	procWNetGetConnectionW = modMpr.NewProc("WNetGetConnectionW")
)

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

// systemDriveResolver asks the multiple provider router what a drive letter is
// mapped to. This is how a WSL share reached through a drive letter — what
// `pushd \\wsl.localhost\...` produces in cmd.exe — becomes recognizable again.
func systemDriveResolver(letter string) string {
	// WNetGetConnectionW expects the local name as a letter and colon, with no
	// trailing separator.
	local, err := windows.UTF16PtrFromString(letter + ":")
	if err != nil {
		return ""
	}

	// A UNC path can exceed MAX_PATH, so grow the buffer if asked to. The
	// length is in characters and, on success, excludes the terminating NUL.
	length := uint32(windows.MAX_PATH)
	for attempt := 0; attempt < 2; attempt++ {
		remote := make([]uint16, length)
		// WNetGetConnectionW returns its error code directly rather than
		// through GetLastError.
		//
		// #nosec G103
		// The unsafe.Pointer conversions have to happen inside the call
		// expression so the garbage collector keeps the referents alive for its
		// duration; hoisting them into variables would be the unsafe version.
		// All three point at memory owned by this function.
		code, _, _ := procWNetGetConnectionW.Call(
			uintptr(unsafe.Pointer(local)),      // #nosec G103
			uintptr(unsafe.Pointer(&remote[0])), // #nosec G103
			uintptr(unsafe.Pointer(&length)),    // #nosec G103
		)
		switch syscall.Errno(code) {
		case 0:
			return windows.UTF16ToString(remote)
		case windows.ERROR_MORE_DATA:
			// length now holds the size required; retry once with it.
			continue
		default:
			// Not a network mapping, disconnected, or otherwise unanswerable.
			return ""
		}
	}
	return ""
}
